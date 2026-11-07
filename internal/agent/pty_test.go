package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ptyConn runs "devenv-agent pty-attach" like the server does over SSH.
type ptyConn struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	info PtySession
	data bytes.Buffer
	exit *int
}

func attach(t *testing.T, bin, tmp string, req PtyAttachRequest) *ptyConn {
	t.Helper()
	cmd := exec.Command(bin, "pty-attach")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "HOME="+tmp)
	in, _ := cmd.StdinPipe()
	outPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, _ := json.Marshal(map[string]any{"op": "pty-attach", "pty_attach": req})
	_, _ = in.Write(append(line, '\n'))
	c := &ptyConn{t: t, cmd: cmd, in: in, out: bufio.NewReader(outPipe)}
	resp, err := c.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("attach reply: %v", err)
	}
	var r Response[PtySession]
	if err := json.Unmarshal(resp, &r); err != nil || !r.OK {
		t.Fatalf("attach: %s %v", resp, err)
	}
	c.info = r.Result
	return c
}

func (c *ptyConn) send(s string) {
	if err := WriteFrame(c.in, FrameData, []byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

// waitFor reads frames until the accumulated output contains want.
func (c *ptyConn) waitFor(want string) {
	c.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(c.data.String(), want) {
		if time.Now().After(deadline) {
			c.t.Fatalf("timeout waiting for %q; got %q", want, c.data.String())
		}
		typ, b, err := ReadFrame(c.out)
		if err != nil {
			c.t.Fatalf("read: %v (output %q)", err, c.data.String())
		}
		switch typ {
		case FrameData:
			c.data.Write(b)
		case FrameExit:
			var e struct{ Code int }
			_ = json.Unmarshal(b, &e)
			c.exit = &e.Code
			if want == "<exit>" {
				return
			}
		}
	}
}

func TestPtySessions(t *testing.T) {
	bin := buildAgent(t)
	tmp := t.TempDir()
	t.Cleanup(func() { exec.Command("pkill", "-f", bin+" daemon").Run() })

	a := attach(t, bin, tmp, PtyAttachRequest{Command: "exec bash --norc -i", Cols: 100, Rows: 30, Name: "main"})
	if a.info.ID == "" || a.info.Cols != 100 {
		t.Fatalf("info %+v", a.info)
	}
	a.send("export PS1='$ '; echo marker-$((40+2))\n")
	a.waitFor("marker-42")
	a.send("stty size\n")
	a.waitFor("30 100")

	// Detach (close the client) and reattach: the shell survives and the
	// scrollback is replayed.
	a.in.Close()
	_ = a.cmd.Wait()
	b := attach(t, bin, tmp, PtyAttachRequest{ID: a.info.ID, Cols: 120, Rows: 40})
	b.waitFor("marker-42")

	// A second client sees the same output.
	c := attach(t, bin, tmp, PtyAttachRequest{ID: a.info.ID, NoReplay: true})
	b.send("echo shared-$((1+1))\n")
	b.waitFor("shared-2")
	c.waitFor("shared-2")
	if err := WriteFrame(b.in, FrameResize, []byte(`{"cols":90,"rows":20}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	b.send("stty size\n")
	b.waitFor("20 90")

	list, errMsg := run[PtyList](t, bin, tmp, "pty-list", struct{}{})
	if errMsg != "" || len(list.Sessions) != 1 || list.Sessions[0].Name != "main" || list.Sessions[0].Clients != 2 {
		t.Fatalf("list %+v %q", list, errMsg)
	}
	if _, errMsg := run[PtySession](t, bin, tmp, "pty-rename", PtyRenameRequest{ID: a.info.ID, Name: "build"}); errMsg != "" {
		t.Fatal(errMsg)
	}

	// Exit ends the session for every client.
	b.send("exit 3\n")
	b.waitFor("<exit>")
	if b.exit == nil || *b.exit != 3 {
		t.Fatalf("exit %v", b.exit)
	}
	c.waitFor("<exit>")
	time.Sleep(200 * time.Millisecond)
	if list, _ := run[PtyList](t, bin, tmp, "pty-list", struct{}{}); len(list.Sessions) != 0 {
		t.Fatalf("session not removed: %+v", list)
	}

	// Kill.
	d := attach(t, bin, tmp, PtyAttachRequest{Command: "sleep 600"})
	if _, errMsg := run[any](t, bin, tmp, "pty-kill", PtyKillRequest{ID: d.info.ID}); errMsg != "" {
		t.Fatal(errMsg)
	}
	d.waitFor("<exit>")

	// Unknown id.
	cmd := exec.Command(bin, "pty-attach")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	cmd.Stdin = strings.NewReader(`{"op":"pty-attach","pty_attach":{"id":"nope"}}` + "\n")
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "not found") {
		t.Fatalf("unknown id: %s", out)
	}
}

func TestListPorts(t *testing.T) {
	ports, err := ListPorts()
	if err != nil {
		t.Fatal(err)
	}
	_ = ports // content depends on the machine; parsing must not fail
	ip, port, err := parseHexAddr("0100007F:1F90")
	if err != nil || ip.String() != "127.0.0.1" || port != 8080 {
		t.Fatalf("parse %v %d %v", ip, port, err)
	}
}
