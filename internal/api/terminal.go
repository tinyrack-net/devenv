package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/agent"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/helper"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

// Terminal protocol (WebSocket):
//   - binary messages: raw terminal bytes in both directions
//   - text messages from the client: JSON control {"type":"resize","cols":N,"rows":N}
//   - text messages from the server:
//     {"type":"session","session":{...}}  attached (persistent sessions)
//     {"type":"exit","code":N}            the shell exited
//     {"type":"error","message":"..."}
//
// Query: cols, rows, session (attach to an existing session id), new=1
// (create a session; with session=<id> creates that id), name, command.
// Persistent sessions need devenv-agent; without it every connection is a
// fresh shell.
type termControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// checkOrigin rejects cross-site WebSocket upgrades. Cookies are sent on
// WebSocket handshakes, so Origin must match the Host.
func checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil // non-browser client
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host != r.Host {
		return errors.New("cross-origin websocket rejected")
	}
	return nil
}

func (s *Server) acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, bool) {
	if err := checkOrigin(r); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return nil, false
	}
	// Origin is validated above; the library check would reject proxies that
	// rewrite Host.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, false
	}
	return c, true
}

func wsSendJSON(ctx context.Context, c *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, b)
}

func dialError(err error) string {
	if errors.Is(err, sshclient.ErrNotRunning) {
		return "environment is not running"
	}
	return "could not connect to environment: " + err.Error()
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, _, err := s.Authorize(r.Context(), principal(r), name, access.Use); err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, _ := strconv.Atoi(q.Get("rows"))
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 500 {
		rows = 24
	}
	ws, ok := s.acceptWS(w, r)
	if !ok {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	log := logf.FromContext(ctx).WithValues("environment", name, "user", principal(r).Name)

	sc, err := s.Dialer.Dial(ctx, name)
	if err != nil {
		if !errors.Is(err, sshclient.ErrNotRunning) {
			log.Error(err, "terminal: connect to environment")
		}
		wsSendJSON(ctx, ws, map[string]string{"type": "error", "message": dialError(err)})
		ws.Close(websocket.StatusNormalClosure, "")
		return
	}
	defer sc.Close()

	err = s.attachSession(ctx, ws, sc, agent.PtyAttachRequest{
		ID: q.Get("session"), Create: q.Get("new") != "" || q.Get("session") == "",
		Name: q.Get("name"), Command: q.Get("command"), Cols: cols, Rows: rows,
	})
	if errors.Is(err, helper.ErrUnavailable) {
		s.plainShell(ctx, ws, sc, cols, rows)
		return
	}
	if err != nil {
		wsSendJSON(ctx, ws, map[string]string{"type": "error", "message": err.Error()})
		ws.Close(websocket.StatusNormalClosure, "")
	}
}

// attachSession bridges the WebSocket to a devenv-agent terminal session.
func (s *Server) attachSession(ctx context.Context, ws *websocket.Conn, sc *ssh.Client, req agent.PtyAttachRequest) error {
	sess, err := sc.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	if err := sess.Start(helper.ShellQuote(helper.Path) + " pty-attach"); err != nil {
		return err
	}
	line, _ := json.Marshal(map[string]any{"op": "pty-attach", "pty_attach": req})
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		return err
	}
	rd := bufio.NewReader(stdout)
	reply, err := rd.ReadBytes('\n')
	if err != nil {
		// The helper binary is missing: the shell reports 127 and closes.
		if werr := sess.Wait(); werr != nil {
			var ee *ssh.ExitError
			if errors.As(werr, &ee) && (ee.ExitStatus() == 127 || ee.ExitStatus() == 126) {
				return helper.ErrUnavailable
			}
		}
		return fmt.Errorf("terminal session: %v", err)
	}
	var resp agent.Response[agent.PtySession]
	if err := json.Unmarshal(reply, &resp); err != nil {
		return fmt.Errorf("terminal session: invalid reply")
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	wsSendJSON(ctx, ws, map[string]any{"type": "session", "session": resp.Result})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Daemon → browser.
	go func() {
		defer cancel()
		for {
			t, b, err := agent.ReadFrame(rd)
			if err != nil {
				return
			}
			switch t {
			case agent.FrameData:
				if err := ws.Write(ctx, websocket.MessageBinary, b); err != nil {
					return
				}
			case agent.FrameExit:
				var e struct{ Code int }
				_ = json.Unmarshal(b, &e)
				wsSendJSON(ctx, ws, map[string]any{"type": "exit", "code": e.Code})
				ws.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}()
	// Browser → daemon.
	ws.SetReadLimit(1 << 20)
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			// Detach; the session keeps running.
			_ = stdin.Close()
			return nil
		}
		if typ == websocket.MessageBinary {
			if err := agent.WriteFrame(stdin, agent.FrameData, data); err != nil {
				return nil
			}
			continue
		}
		var ctl termControl
		if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
			b, _ := json.Marshal(map[string]int{"cols": ctl.Cols, "rows": ctl.Rows})
			if err := agent.WriteFrame(stdin, agent.FrameResize, b); err != nil {
				return nil
			}
		}
	}
}

// plainShell runs a login shell for this connection only (no helper).
func (s *Server) plainShell(ctx context.Context, ws *websocket.Conn, sc *ssh.Client, cols, rows int) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sess, err := sc.NewSession()
	if err != nil {
		wsSendJSON(ctx, ws, map[string]string{"type": "error", "message": err.Error()})
		return
	}
	defer sess.Close()
	_ = sess.Setenv("COLORTERM", "truecolor")
	if err := sess.RequestPty("xterm-256color", rows, cols, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
		wsSendJSON(ctx, ws, map[string]string{"type": "error", "message": "pty: " + err.Error()})
		return
	}
	stdin, _ := sess.StdinPipe()
	stdout, _ := sess.StdoutPipe()
	stderr, _ := sess.StderrPipe()
	if err := sess.Shell(); err != nil {
		wsSendJSON(ctx, ws, map[string]string{"type": "error", "message": "shell: " + err.Error()})
		return
	}
	out := make(chan []byte, 16)
	pump := func(rd io.Reader) {
		buf := make([]byte, 32*1024)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				b := append([]byte(nil), buf[:n]...)
				select {
				case out <- b:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout)
	go pump(stderr)
	go func() {
		for {
			select {
			case b := <-out:
				if err := ws.Write(ctx, websocket.MessageBinary, b); err != nil {
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer cancel()
		ws.SetReadLimit(1 << 20)
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := stdin.Write(data); err != nil {
					return
				}
				continue
			}
			var ctl termControl
			if json.Unmarshal(data, &ctl) == nil && ctl.Type == "resize" && ctl.Cols > 0 && ctl.Rows > 0 {
				_ = sess.WindowChange(ctl.Rows, ctl.Cols)
			}
		}
	}()
	waitErr := make(chan error, 1)
	go func() { waitErr <- sess.Wait() }()
	select {
	case err := <-waitErr:
		time.Sleep(100 * time.Millisecond)
		code := 0
		var ee *ssh.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitStatus()
		}
		wsSendJSON(ctx, ws, map[string]any{"type": "exit", "code": code})
		ws.Close(websocket.StatusNormalClosure, "")
	case <-ctx.Done():
	}
}

// vnc tunnels a WebSocket (noVNC, binary RFB) to the environment's VNC
// server through SSH, so VNC only listens on localhost inside the environment.
func (s *Server) vnc(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	env, _, err := s.Authorize(r.Context(), principal(r), name, access.Use)
	if err != nil {
		fail(w, r, err)
		return
	}
	if !env.Status.Desktop {
		writeError(w, http.StatusBadRequest, "image does not provide a desktop")
		return
	}
	ws, ok := s.acceptWS(w, r)
	if !ok {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	sc, err := s.Dialer.Dial(ctx, name)
	if err != nil {
		if !errors.Is(err, sshclient.ErrNotRunning) {
			logf.FromContext(ctx).Error(err, "vnc: connect to environment", "environment", name)
		}
		ws.Close(websocket.StatusTryAgainLater, truncate(dialError(err), 120))
		return
	}
	defer sc.Close()
	var vncConn net.Conn
	for attempt := 0; ; attempt++ {
		vncConn, err = sc.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", controller.VNCPort))
		if err == nil || attempt >= 10 || ctx.Err() != nil {
			break
		}
		time.Sleep(500 * time.Millisecond) // desktop may still be starting
	}
	if err != nil {
		ws.Close(websocket.StatusTryAgainLater, "VNC server not reachable")
		return
	}
	defer vncConn.Close()

	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	go func() {
		_, _ = io.Copy(vncConn, nc)
		cancel()
	}()
	_, _ = io.Copy(nc, vncConn)
	ws.Close(websocket.StatusNormalClosure, "")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
