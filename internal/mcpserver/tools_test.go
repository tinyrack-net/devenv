package mcpserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// newEnvHarness creates a running MCP environment "w".
func newEnvHarness(t *testing.T) *harness {
	h := newHarness(t)
	if isErr, text := h.call("create_environment", map[string]any{"name": "w", "image": "base"}, nil); isErr {
		t.Fatalf("create: %s", text)
	}
	return h
}

func TestReadWriteEdit(t *testing.T) {
	h := newEnvHarness(t)

	var w writeOut
	if isErr, text := h.call("write_file", map[string]any{"name": "w", "path": "proj/src/main.go", "content": "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"}, &w); isErr {
		t.Fatalf("write: %s", text)
	}
	if !w.Created || w.Path != filepath.Join(homeDir, "proj/src/main.go") {
		t.Errorf("write = %+v", w)
	}

	var r readOut
	h.call("read_file", map[string]any{"name": "w", "path": "~/proj/src/main.go"}, &r)
	if r.TotalLines != 5 || !strings.Contains(r.Content, "     4\t\tprintln(\"hi\")\n") || r.Truncated {
		t.Errorf("read = %+v", r)
	}
	h.call("read_file", map[string]any{"name": "w", "path": "proj/src/main.go", "offset": 3, "limit": 1}, &r)
	if r.Content != "     3\tfunc main() {\n" || r.StartLine != 3 || r.EndLine != 3 || !r.Truncated {
		t.Errorf("read range = %+v", r)
	}

	var e editOut
	if isErr, text := h.call("edit_file", map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{
		{"old_text": "\tprintln(\"hi\")", "new_text": "\tprintln(\"hello\")\n\tprintln(\"world\")"},
		{"old_text": "package main", "new_text": "package main // edited"},
	}}, &e); isErr {
		t.Fatalf("edit: %s", text)
	}
	if e.Replacements != 2 || !strings.Contains(e.Diff, "-\tprintln(\"hi\")") || !strings.Contains(e.Diff, "+\tprintln(\"world\")") {
		t.Errorf("edit = %+v", e)
	}
	got, _ := os.ReadFile(filepath.Join(homeDir, "proj/src/main.go"))
	if string(got) != "package main // edited\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"world\")\n}\n" {
		t.Errorf("file = %q", got)
	}

	// Errors the agent must see clearly.
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{{"old_text": "nope", "new_text": "x"}}}, "not found"},
		{map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{{"old_text": "println", "new_text": "x"}}}, "matches 2 places"},
		{map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{{"old_text": "  println(\"hello\")", "new_text": "x"}}}, "whitespace"},
		{map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{{"old_text": "func main", "new_text": "a"}, {"old_text": "main() {", "new_text": "b"}}}, "overlap"},
		{map[string]any{"name": "w", "path": "missing.txt", "edits": []map[string]any{{"old_text": "a", "new_text": "b"}}}, "no such file"},
	}
	for _, c := range cases {
		if isErr, text := h.call("edit_file", c.args, nil); !isErr || !strings.Contains(text, c.want) {
			t.Errorf("edit %v: %v %q, want %q", c.args["edits"], isErr, text, c.want)
		}
	}
	var all editOut
	h.call("edit_file", map[string]any{"name": "w", "path": "proj/src/main.go", "edits": []map[string]any{{"old_text": "println", "new_text": "print", "replace_all": true}}}, &all)
	if all.Replacements != 2 {
		t.Errorf("replace_all = %+v", all)
	}

	// Binary and directory handling.
	os.WriteFile(filepath.Join(homeDir, "bin.dat"), []byte{0, 1, 2, 3}, 0o644)
	if isErr, text := h.call("read_file", map[string]any{"name": "w", "path": "bin.dat"}, nil); !isErr || !strings.Contains(text, "binary") {
		t.Errorf("binary read: %v %s", isErr, text)
	}
	if isErr, text := h.call("read_file", map[string]any{"name": "w", "path": "proj"}, nil); !isErr || !strings.Contains(text, "directory") {
		t.Errorf("dir read: %v %s", isErr, text)
	}

	// Mode is kept on overwrite.
	script := filepath.Join(homeDir, "run.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho a\n"), 0o755)
	h.call("edit_file", map[string]any{"name": "w", "path": "run.sh", "edits": []map[string]any{{"old_text": "echo a", "new_text": "echo b"}}}, nil)
	if st, _ := os.Stat(script); st.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}

	var l listOut
	h.call("list_dir", map[string]any{"name": "w"}, &l)
	if len(l.Entries) < 3 || l.Entries[0].Type != "dir" || l.Entries[0].Name != "proj" {
		t.Errorf("list = %+v", l.Entries)
	}
}

func TestCRLFEdit(t *testing.T) {
	h := newEnvHarness(t)
	p := filepath.Join(homeDir, "win.txt")
	os.WriteFile(p, []byte("a\r\nb\r\nc\r\n"), 0o644)
	if isErr, text := h.call("edit_file", map[string]any{"name": "w", "path": "win.txt", "edits": []map[string]any{{"old_text": "a\nb", "new_text": "a\nB"}}}, nil); isErr {
		t.Fatalf("crlf edit: %s", text)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "a\r\nB\r\nc\r\n" {
		t.Errorf("crlf file = %q", got)
	}
}

func TestSearchAndFind(t *testing.T) {
	h := newEnvHarness(t)
	for p, c := range map[string]string{
		"repo/a.go":              "package a\nfunc Hello() {}\n",
		"repo/sub/b.go":          "package sub\n// hello world\n",
		"repo/readme.md":         "Hello docs\n",
		"repo/.git/HEAD":         "ref: hello\n",
		"repo/ignored/x.go":      "func Hello() {}\n",
		"repo/.gitignore":        "ignored/\n",
		"repo/node_modules/m.js": "hello\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(homeDir, p)), 0o755)
		os.WriteFile(filepath.Join(homeDir, p), []byte(c), 0o644)
	}
	// rg needs a git repo to apply .gitignore.
	exec.Command("git", "-C", filepath.Join(homeDir, "repo"), "init", "-q").Run()

	var s searchOut
	if isErr, text := h.call("search", map[string]any{"name": "w", "pattern": "hello", "path": "repo", "ignore_case": true, "glob": "*.go"}, &s); isErr {
		t.Fatalf("search: %s", text)
	}
	if !strings.Contains(s.Results, "a.go:2:func Hello() {}") || !strings.Contains(s.Results, "sub/b.go:2:// hello world") {
		t.Errorf("search results = %q (engine %s)", s.Results, s.Engine)
	}
	if s.Engine == "rg" && strings.Contains(s.Results, "ignored/") {
		t.Errorf("rg should respect .gitignore: %q", s.Results)
	}
	if strings.Contains(s.Results, ".git/") || strings.Contains(s.Results, "node_modules") {
		t.Errorf("search must skip .git and node_modules: %q", s.Results)
	}

	h.call("search", map[string]any{"name": "w", "pattern": "Hello", "path": "repo", "files_with_matches": true, "fixed_strings": true}, &s)
	if !strings.Contains(s.Results, "readme.md") || strings.Contains(s.Results, ":") {
		t.Errorf("files_with_matches = %q", s.Results)
	}
	h.call("search", map[string]any{"name": "w", "pattern": "e", "path": "repo", "ignore_case": true, "max_results": 2}, &s)
	if s.Count != 2 || !s.Truncated {
		t.Errorf("truncation = %+v", s)
	}
	h.call("search", map[string]any{"name": "w", "pattern": "zzz-no-match", "path": "repo"}, &s)
	if s.Count != 0 || s.Results != "" {
		t.Errorf("no match = %+v", s)
	}
	if isErr, text := h.call("search", map[string]any{"name": "w", "pattern": "(", "path": "repo"}, nil); !isErr || !strings.Contains(text, "search failed") {
		t.Errorf("bad regex: %v %s", isErr, text)
	}

	var f findOut
	h.call("find_files", map[string]any{"name": "w", "path": "repo", "pattern": "*.go"}, &f)
	joined := strings.Join(f.Files, ",")
	if !strings.Contains(joined, "a.go") || !strings.Contains(joined, "sub/b.go") || strings.Contains(joined, "node_modules") {
		t.Errorf("find = %v", f.Files)
	}
}

func TestProcesses(t *testing.T) {
	h := newEnvHarness(t)

	var st startProcOut
	if isErr, text := h.call("start_process", map[string]any{"name": "w", "id": "server",
		"command": "echo starting; sleep 1; echo 'listening on :8080'; sleep 60", "wait_seconds": 10, "wait_for": "listening on"}, &st); isErr {
		t.Fatalf("start: %s", text)
	}
	if !st.Process.Running || !strings.Contains(st.Output, "listening on :8080") || st.Process.Command == "" {
		t.Errorf("start = %+v", st)
	}
	if isErr, text := h.call("start_process", map[string]any{"name": "w", "id": "server", "command": "true"}, nil); !isErr || !strings.Contains(text, "already running") {
		t.Errorf("duplicate id: %v %s", isErr, text)
	}

	var rd readProcOut
	h.call("read_process_output", map[string]any{"name": "w", "id": "server"}, &rd)
	if !strings.Contains(rd.Output, "starting") || rd.NextOffset == 0 {
		t.Errorf("read tail = %+v", rd)
	}
	next := rd.NextOffset
	h.call("read_process_output", map[string]any{"name": "w", "id": "server", "offset": next, "wait_seconds": 1}, &rd)
	if rd.Output != "" || rd.Offset != next {
		t.Errorf("follow without new output = %+v", rd)
	}

	// A short job that exits with a code.
	h.call("start_process", map[string]any{"name": "w", "id": "job", "command": "echo done; exit 7", "wait_seconds": 10}, &st)
	if st.Process.Running || st.Process.ExitCode == nil || *st.Process.ExitCode != 7 || !strings.Contains(st.Output, "done") {
		t.Errorf("job = %+v", st)
	}

	var ls listProcOut
	h.call("list_processes", map[string]any{"name": "w"}, &ls)
	if len(ls.Processes) != 2 {
		t.Errorf("list = %+v", ls)
	}

	var stop startProcOut
	start := time.Now()
	h.call("stop_process", map[string]any{"name": "w", "id": "server", "remove": true}, &stop)
	if stop.Process.Running || time.Since(start) > 10*time.Second {
		t.Errorf("stop = %+v after %v", stop.Process, time.Since(start))
	}
	pid := stop.Process.PID
	if pid > 0 && exec.Command("kill", "-0", "--", "-"+strconv.Itoa(pid)).Run() == nil {
		t.Errorf("process group %d still alive", pid)
	}
	h.call("list_processes", map[string]any{"name": "w"}, &ls)
	if len(ls.Processes) != 1 || ls.Processes[0].ID != "job" {
		t.Errorf("after remove = %+v", ls)
	}
	if isErr, _ := h.call("read_process_output", map[string]any{"name": "w", "id": "server"}, nil); !isErr {
		t.Error("removed process should be gone")
	}
	if isErr, _ := h.call("start_process", map[string]any{"name": "w", "id": "Bad Id", "command": "true"}, nil); !isErr {
		t.Error("invalid id accepted")
	}
}
