package api

import (
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

	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

// Terminal protocol (WebSocket):
//   - binary messages: raw terminal bytes in both directions
//   - text messages from the client: JSON control {"type":"resize","cols":N,"rows":N}
//   - text messages from the server: JSON {"type":"exit","code":N} or {"type":"error","message":"..."}
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
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
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
	log := logf.FromContext(ctx).WithValues("environment", name)

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
	log.V(1).Info("terminal opened")
	s.keepActive(ctx, name)

	// Output: stdout+stderr -> websocket.
	out := make(chan []byte, 16)
	pump := func(rd io.Reader) {
		buf := make([]byte, 32*1024)
		for {
			n, err := rd.Read(buf)
			if n > 0 {
				b := make([]byte, n)
				copy(b, buf[:n])
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

	// Input: websocket -> stdin / resize.
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
		// Let buffered output flush.
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
	env, err := s.getEnv(r.Context(), name)
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
	s.keepActive(ctx, name)

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
