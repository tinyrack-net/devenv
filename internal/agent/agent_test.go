package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExec(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		req  ExecRequest
		chk  func(t *testing.T, r ExecResult)
	}{
		{"exit code", ExecRequest{Command: "echo out; echo err >&2; exit 7"}, func(t *testing.T, r ExecResult) {
			if r.ExitCode != 7 || r.Stdout != "out\n" || r.Stderr != "err\n" {
				t.Errorf("%+v", r)
			}
		}},
		{"signal", ExecRequest{Command: "kill -SEGV $$"}, func(t *testing.T, r ExecResult) {
			if r.ExitCode != -1 || r.Signal != "SEGV" {
				t.Errorf("%+v", r)
			}
		}},
		{"env cwd stdin", ExecRequest{Command: `printf '%s|%s|' "$FOO" "$(pwd)"; cat`, Cwd: "/tmp", Env: map[string]string{"FOO": "it's"}, Stdin: "piped"}, func(t *testing.T, r ExecResult) {
			if r.Stdout != "it's|/tmp|piped" {
				t.Errorf("%q", r.Stdout)
			}
		}},
		{"truncation", ExecRequest{Command: "head -c 10000 /dev/zero | tr '\\0' a", MaxOutput: 1000}, func(t *testing.T, r ExecResult) {
			if !r.Truncated || len(r.Stdout) > 1100 || !strings.Contains(r.Stdout, "bytes omitted") {
				t.Errorf("len %d truncated %v", len(r.Stdout), r.Truncated)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Exec(ctx, c.req)
			if err != nil {
				t.Fatal(err)
			}
			c.chk(t, r)
		})
	}
	if _, err := Exec(ctx, ExecRequest{Command: "true", Env: map[string]string{"BAD-NAME": "x"}}); err == nil {
		t.Error("invalid env name accepted")
	}
	if _, err := Exec(ctx, ExecRequest{Command: "true", Cwd: "/nonexistent"}); err == nil {
		t.Error("missing cwd accepted")
	}
}

// Timeout must kill the whole tree, including background children.
func TestExecTimeoutKillsGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	r, err := Exec(context.Background(), ExecRequest{
		Command:   "sleep 300 & echo $! > " + marker + "; sleep 300",
		TimeoutMs: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.TimedOut || r.ExitCode != -1 || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v after %v", r, time.Since(start))
	}
	b, _ := os.ReadFile(marker)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(200 * time.Millisecond)
	if pid > 0 && exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil {
		t.Errorf("background child %d survived the timeout", pid)
	}
}

// Background children of a finished command keep running (exec "app &"),
// and exec returns promptly even though they hold the output pipes.
func TestExecKeepsBackgroundChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	r, err := Exec(context.Background(), ExecRequest{Command: "sleep 300 & echo $! > " + marker + "; echo started"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stdout != "started\n" || time.Since(start) > 5*time.Second {
		t.Errorf("%+v after %v", r, time.Since(start))
	}
	b, _ := os.ReadFile(marker)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid <= 0 || exec.Command("kill", "-0", strconv.Itoa(pid)).Run() != nil {
		t.Fatalf("background child %d should still run", pid)
	}
	exec.Command("kill", strconv.Itoa(pid)).Run()
}

func writeTree(t *testing.T, root string, files map[string]string) {
	for p, c := range files {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSearch(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.go":               "package a\nfunc Hello() {}\n",
		"sub/b.go":           "package sub\n// hello world\nline3\nline4\nhello again\n",
		"readme.md":          "Hello docs\n",
		".git/HEAD":          "hello\n",
		"node_modules/m.js":  "hello\n",
		"ignored/x.go":       "hello\n",
		"keep/z.log":         "hello\n",
		"keep/important.log": "hello\n",
		".gitignore":         "ignored/\n*.log\n!important.log\n",
		".hidden/secret.txt": "hello\n",
		"bin.dat":            "hel\x00lo hello\n",
	})

	r, err := Search(SearchRequest{Pattern: "hello", Path: root, IgnoreCase: true, MaxResults: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.go:2:func Hello() {}", "sub/b.go:2:// hello world", "sub/b.go:5:hello again", "readme.md:1:Hello docs", "keep/important.log:1:hello"}
	for _, w := range want {
		if !strings.Contains(r.Results, w) {
			t.Errorf("missing %q in\n%s", w, r.Results)
		}
	}
	for _, bad := range []string{".git", "node_modules", "ignored/", "z.log", ".hidden", "bin.dat"} {
		if strings.Contains(r.Results, bad) {
			t.Errorf("%s should be skipped:\n%s", bad, r.Results)
		}
	}

	r, _ = Search(SearchRequest{Pattern: "hello", Path: root, IncludeIgnored: true, MaxResults: 100})
	if !strings.Contains(r.Results, "ignored/x.go") || !strings.Contains(r.Results, ".hidden/secret.txt") {
		t.Errorf("include_ignored:\n%s", r.Results)
	}

	r, _ = Search(SearchRequest{Pattern: "hello", Path: root, Glob: "*.go", IgnoreCase: true, MaxResults: 100})
	if strings.Contains(r.Results, "readme.md") || !strings.Contains(r.Results, "a.go") {
		t.Errorf("glob:\n%s", r.Results)
	}

	r, _ = Search(SearchRequest{Pattern: "line3", Path: root, Context: 1, MaxResults: 100})
	if r.Results != "sub/b.go-2-// hello world\nsub/b.go:3:line3\nsub/b.go-4-line4" {
		t.Errorf("context:\n%s", r.Results)
	}

	r, _ = Search(SearchRequest{Pattern: "hello", Path: root, FilesOnly: true, IgnoreCase: true, MaxResults: 100})
	if strings.Contains(r.Results, ":") || !strings.Contains(r.Results, "sub/b.go") {
		t.Errorf("files only:\n%s", r.Results)
	}

	r, _ = Search(SearchRequest{Pattern: "e", Path: root, MaxResults: 2})
	if r.Count != 2 || !r.Truncated {
		t.Errorf("truncation: %+v", r)
	}

	if _, err := Search(SearchRequest{Pattern: "(", Path: root, MaxResults: 10}); err == nil || !strings.Contains(err.Error(), "invalid regular expression") {
		t.Errorf("bad regex: %v", err)
	}
	r, _ = Search(SearchRequest{Pattern: "Hello()", Path: root, FixedStrings: true, MaxResults: 10})
	if r.Results != "a.go:2:func Hello() {}" {
		t.Errorf("fixed string: %+v", r)
	}
	r, _ = Search(SearchRequest{Pattern: "Hello", Path: filepath.Join(root, "a.go"), MaxResults: 10})
	if !strings.Contains(r.Results, ":2:func Hello() {}") {
		t.Errorf("single file: %q", r.Results)
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"a.go": "", "x/b.go": "", "x/y/c.ts": "", "node_modules/d.go": "", ".gitignore": "x/y/\n"})
	r, err := Find(FindRequest{Pattern: "*.go", Path: root, MaxResults: 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(r.Files, ",") != "a.go,x/b.go" {
		t.Errorf("find = %v", r.Files)
	}
	r, _ = Find(FindRequest{Pattern: "x/**", Path: root, MaxResults: 100, IncludeIgnored: true})
	if strings.Join(r.Files, ",") != "x/b.go,x/y/c.ts" {
		t.Errorf("find doublestar = %v", r.Files)
	}
}

func TestRingLog(t *testing.T) {
	r := &ringLog{}
	r.Write([]byte("hello "))
	r.Write([]byte("world"))
	if s, off, _ := r.read(6, 100); s != "world" || off != 6 {
		t.Errorf("%q %d", s, off)
	}
	if s, off, _ := r.read(-1, 3); s != "rld" || off != 8 {
		t.Errorf("tail %q %d", s, off)
	}
	big := make([]byte, maxLogBytes)
	r.Write(big)
	if _, off, dropped := r.read(0, 10); !dropped || off != 11 {
		t.Errorf("dropped offset %d %v", off, dropped)
	}
}

func TestStats(t *testing.T) {
	s, err := Stats(StatsRequest{Path: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if s.DiskTotal == 0 || s.DiskUsed > s.DiskTotal || s.Processes == 0 {
		t.Errorf("%+v", s)
	}
}
