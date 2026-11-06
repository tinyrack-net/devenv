package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// buildAgent compiles the helper so the daemon can be spawned like in an environment.
func buildAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "devenv-agent")
	out, err := exec.Command("go", "build", "-o", bin, "../../cmd/devenv-agent").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

// run invokes the helper like the server does: op argument, JSON on stdin.
func run[T any](t *testing.T, bin, tmp, op string, req any) (T, string) {
	t.Helper()
	var zero T
	b, _ := json.Marshal(req)
	cmd := exec.Command(bin, op)
	cmd.Stdin = bytes.NewReader(b)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	var resp Response[T]
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("%s: decode %s: %v", op, out, err)
	}
	if !resp.OK {
		return zero, resp.Error
	}
	return resp.Result, ""
}

func TestDaemonProcesses(t *testing.T) {
	bin := buildAgent(t)
	tmp := t.TempDir()
	t.Cleanup(func() {
		// Stop the daemon spawned for this test.
		exec.Command("pkill", "-f", bin+" daemon").Run()
	})

	// Start a server-like process and wait for readiness text.
	out, errMsg := run[Output](t, bin, tmp, "start", StartRequest{ID: "web", Command: "echo starting; sleep 0.5; echo 'listening on :8080'; sleep 60", WaitMs: 5000, WaitFor: "listening on", Limit: 1000})
	if errMsg != "" || !out.Process.Running || !strings.Contains(out.Output, "listening on :8080") {
		t.Fatalf("start: %q %+v", errMsg, out)
	}
	// Duplicate id is rejected while running.
	if _, errMsg := run[Output](t, bin, tmp, "start", StartRequest{ID: "web", Command: "true"}); !strings.Contains(errMsg, "already running") {
		t.Errorf("duplicate: %q", errMsg)
	}

	// Follow: no new output within the wait returns empty at the same offset.
	next := out.NextOffset
	start := time.Now()
	out, _ = run[Output](t, bin, tmp, "read", ReadRequest{ID: "web", Offset: next, Limit: 1000, WaitMs: 700})
	if out.Output != "" || out.Offset != next || time.Since(start) < 600*time.Millisecond {
		t.Errorf("follow: %+v after %v", out, time.Since(start))
	}

	// A job with an exit code; wait returns as soon as it exits.
	start = time.Now()
	out, _ = run[Output](t, bin, tmp, "start", StartRequest{ID: "job", Command: "echo done; exit 3", WaitMs: 10000, Limit: 1000})
	if out.Process.Running || out.Process.ExitCode == nil || *out.Process.ExitCode != 3 || out.Output != "done\n" || time.Since(start) > 3*time.Second {
		t.Errorf("job: %+v after %v", out, time.Since(start))
	}

	list, _ := run[ProcessList](t, bin, tmp, "list", struct{}{})
	if len(list.Processes) != 2 {
		t.Errorf("list: %+v", list)
	}

	// Stop kills the whole group, including a background grandchild.
	marker := filepath.Join(tmp, "child")
	run[Output](t, bin, tmp, "start", StartRequest{ID: "tree", Command: "sleep 300 & echo $! > " + marker + "; wait", Limit: 100})
	time.Sleep(300 * time.Millisecond)
	stopped, _ := run[Output](t, bin, tmp, "stop", StopRequest{ID: "tree", Remove: true})
	if stopped.Process.Running {
		t.Errorf("stop: %+v", stopped.Process)
	}
	b, _ := os.ReadFile(marker)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(200 * time.Millisecond)
	if pid > 0 && exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil {
		t.Errorf("grandchild %d survived stop", pid)
	}
	if _, errMsg := run[Output](t, bin, tmp, "read", ReadRequest{ID: "tree", Offset: -1, Limit: 10}); !strings.Contains(errMsg, "not found") {
		t.Errorf("removed: %q", errMsg)
	}
	run[Output](t, bin, tmp, "stop", StopRequest{ID: "web"})
	if _, errMsg := run[Output](t, bin, tmp, "start", StartRequest{ID: "Bad Id", Command: "true"}); !strings.Contains(errMsg, "invalid process id") {
		t.Errorf("bad id: %q", errMsg)
	}
}
