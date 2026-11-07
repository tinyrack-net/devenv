package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Terminal sessions live in the daemon so they survive the SSH session (and
// browser tab) that opened them. Clients attach with
//
//	devenv-agent pty-attach
//
// which writes one JSON line (PtyAttachRequest) to the daemon socket and then
// pipes stdin/stdout to it unchanged. After the JSON reply line, both
// directions carry frames: 1 byte type, 4 byte big endian length, payload.
const (
	FrameData   byte = 0 // terminal bytes (both directions)
	FrameResize byte = 1 // client→daemon JSON {"cols":N,"rows":N}
	FrameExit   byte = 2 // daemon→client JSON {"code":N}
	FrameInfo   byte = 3 // daemon→client JSON PtySession (title, size changes)

	maxPtySessions = 32
	scrollback     = 512 << 10
	maxFrame       = 1 << 20
)

// PtySession describes a terminal session.
type PtySession struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Command   string    `json:"command,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	PID       int       `json:"pid"`
	Cols      int       `json:"cols"`
	Rows      int       `json:"rows"`
	Clients   int       `json:"clients"`
	CreatedAt time.Time `json:"created_at"`
	// LastActivity is the time of the last output or input.
	LastActivity time.Time `json:"last_activity"`
}

// PtyAttachRequest attaches to (or creates) a session.
type PtyAttachRequest struct {
	// ID of an existing session. Empty creates a new one.
	ID string `json:"id,omitempty"`
	// Create a session with this ID when it does not exist.
	Create  bool              `json:"create,omitempty"`
	Name    string            `json:"name,omitempty"`
	Command string            `json:"command,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cols    int               `json:"cols"`
	Rows    int               `json:"rows"`
	// NoReplay skips the scrollback replay.
	NoReplay bool `json:"no_replay,omitempty"`
}

type PtyKillRequest struct {
	ID string `json:"id"`
}

type PtyRenameRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PtyList struct {
	Sessions []PtySession `json:"sessions"`
}

type ptyClient struct {
	w  io.Writer
	mu sync.Mutex
}

func (c *ptyClient) frame(t byte, b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.w, t, b)
}

type ptySession struct {
	mu      sync.Mutex
	info    PtySession
	f       *os.File
	cmd     *exec.Cmd
	buf     []byte
	trimmed bool
	clients map[*ptyClient]struct{}
	done    chan struct{}
	code    int
}

// WriteFrame writes one frame.
func WriteFrame(w io.Writer, t byte, b []byte) error {
	var h [5]byte
	h[0] = t
	binary.BigEndian.PutUint32(h[1:], uint32(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(h[1:])
	if n > maxFrame {
		return 0, nil, fmt.Errorf("frame too large (%d)", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, err
	}
	return h[0], b, nil
}

type ptyRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ptySession
	seq      int
}

func clampSize(cols, rows int) (int, int) {
	if cols <= 0 || cols > 1000 {
		cols = 80
	}
	if rows <= 0 || rows > 500 {
		rows = 24
	}
	return cols, rows
}

func (r *ptyRegistry) create(req PtyAttachRequest) (*ptySession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*ptySession{}
	}
	if len(r.sessions) >= maxPtySessions {
		return nil, fmt.Errorf("too many terminal sessions (%d); close some", len(r.sessions))
	}
	id := req.ID
	if id == "" {
		for {
			r.seq++
			id = fmt.Sprintf("t%d", r.seq)
			if r.sessions[id] == nil {
				break
			}
		}
	} else if !idRe.MatchString(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	} else if r.sessions[id] != nil {
		return r.sessions[id], nil
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = homeDir()
	}
	var cmd *exec.Cmd
	if req.Command != "" {
		cmd = exec.Command("bash", "-lc", req.Command)
	} else {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/bash"
		}
		cmd = exec.Command(shell, "-l")
	}
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "DEVENV_TERMINAL="+id)
	for k, v := range req.Env {
		if validEnvName(k) {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cols, rows := clampSize(req.Cols, req.Rows)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	name := req.Name
	if name == "" {
		name = id
	}
	now := time.Now().UTC()
	s := &ptySession{
		info: PtySession{ID: id, Name: name, Command: req.Command, Cwd: cwd, PID: cmd.Process.Pid,
			Cols: cols, Rows: rows, CreatedAt: now, LastActivity: now},
		f: f, cmd: cmd, clients: map[*ptyClient]struct{}{}, done: make(chan struct{}),
	}
	r.sessions[id] = s
	go s.pump()
	go func() {
		_ = cmd.Wait()
		code, _ := exitInfo(cmd.ProcessState)
		killGroup(cmd.Process.Pid, syscall.SIGHUP)
		s.mu.Lock()
		s.code = code
		s.mu.Unlock()
		// Let pump drain the remaining output before announcing the exit.
		time.Sleep(100 * time.Millisecond)
		_ = f.Close()
		b, _ := json.Marshal(map[string]int{"code": code})
		s.mu.Lock()
		for c := range s.clients {
			_ = c.frame(FrameExit, b)
		}
		s.mu.Unlock()
		close(s.done)
		r.mu.Lock()
		if r.sessions[id] == s {
			delete(r.sessions, id)
		}
		r.mu.Unlock()
	}()
	return s, nil
}

func (s *ptySession) pump() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.f.Read(buf)
		if n > 0 {
			b := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.buf = append(s.buf, b...)
			if over := len(s.buf) - scrollback; over > 0 {
				s.buf = append([]byte(nil), s.buf[over:]...)
				s.trimmed = true
			}
			s.info.LastActivity = time.Now().UTC()
			for c := range s.clients {
				// A slow client must not block the terminal: drop it.
				if err := c.frame(FrameData, b); err != nil {
					delete(s.clients, c)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// replay returns the scrollback, starting after the first line break when
// older output was dropped (so it does not start inside an escape sequence).
func (s *ptySession) replay() []byte {
	b := s.buf
	if s.trimmed {
		if i := bytes.IndexByte(b, '\n'); i >= 0 && i < len(b)-1 {
			b = b[i+1:]
		}
	}
	return append([]byte(nil), b...)
}

func (s *ptySession) snapshot() PtySession {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.info
	out.Clients = len(s.clients)
	return out
}

func (r *ptyRegistry) get(id string) *ptySession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *ptyRegistry) list() PtyList {
	r.mu.Lock()
	ss := make([]*ptySession, 0, len(r.sessions))
	for _, s := range r.sessions {
		ss = append(ss, s)
	}
	r.mu.Unlock()
	out := PtyList{Sessions: []PtySession{}}
	for _, s := range ss {
		out.Sessions = append(out.Sessions, s.snapshot())
	}
	sort.Slice(out.Sessions, func(i, j int) bool { return out.Sessions[i].CreatedAt.Before(out.Sessions[j].CreatedAt) })
	return out
}

func (r *ptyRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sessions)
}

func (r *ptyRegistry) kill(id string) error {
	s := r.get(id)
	if s == nil {
		return fmt.Errorf("terminal session %q not found", id)
	}
	killGroup(s.info.PID, syscall.SIGHUP)
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
		killGroup(s.info.PID, syscall.SIGKILL)
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (r *ptyRegistry) rename(req PtyRenameRequest) (PtySession, error) {
	s := r.get(req.ID)
	if s == nil {
		return PtySession{}, fmt.Errorf("terminal session %q not found", req.ID)
	}
	if len(req.Name) > 100 {
		return PtySession{}, errors.New("name too long")
	}
	s.mu.Lock()
	s.info.Name = req.Name
	s.mu.Unlock()
	return s.snapshot(), nil
}

// attach serves a client connection after the request line was read.
func (r *ptyRegistry) attach(c net.Conn, rd *bufio.Reader, req PtyAttachRequest) {
	var s *ptySession
	var err error
	if req.ID != "" && !req.Create {
		if s = r.get(req.ID); s == nil {
			err = fmt.Errorf("terminal session %q not found (it ends when its shell exits or the environment restarts)", req.ID)
		}
	} else {
		s, err = r.create(req)
	}
	if err != nil {
		writeResp[any](c, nil, err)
		return
	}
	cl := &ptyClient{w: c}
	s.mu.Lock()
	writeResp(c, s.info, nil)
	if !req.NoReplay {
		if b := s.replay(); len(b) > 0 {
			_ = cl.frame(FrameData, b)
		}
	}
	s.clients[cl] = struct{}{}
	s.mu.Unlock()
	if req.Cols > 0 && req.Rows > 0 {
		s.resize(req.Cols, req.Rows)
	}
	defer func() {
		s.mu.Lock()
		delete(s.clients, cl)
		s.mu.Unlock()
	}()
	go func() {
		<-s.done
		_ = c.SetReadDeadline(time.Now())
	}()
	for {
		t, b, err := ReadFrame(rd)
		if err != nil {
			return
		}
		switch t {
		case FrameData:
			s.mu.Lock()
			s.info.LastActivity = time.Now().UTC()
			s.mu.Unlock()
			if _, err := s.f.Write(b); err != nil {
				return
			}
		case FrameResize:
			var sz struct{ Cols, Rows int }
			if json.Unmarshal(b, &sz) == nil {
				s.resize(sz.Cols, sz.Rows)
			}
		}
	}
}

func (s *ptySession) resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.info.Cols == cols && s.info.Rows == rows {
		return
	}
	s.info.Cols, s.info.Rows = cols, rows
	_ = pty.Setsize(s.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// ---- client side

// PtyAttach connects stdin/stdout to a daemon session. The request is read
// as the first line of stdin and forwarded; everything after is piped.
func PtyAttach(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	c, err := dialDaemon(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	// The output side decides when the attach ends: the daemon closes the
	// connection after the exit frame or an error reply. Closed stdin (the
	// client detached) half-closes the socket so the daemon notices.
	go func() {
		_, _ = io.Copy(c, stdin)
		if uc, ok := c.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
	}()
	_, _ = io.Copy(stdout, c)
	return nil
}

func ListPty(ctx context.Context) (PtyList, error) {
	return daemonCall[PtyList](ctx, wireRequest{Op: "pty-list"})
}

func KillPty(ctx context.Context, req PtyKillRequest) (any, error) {
	return daemonCall[any](ctx, wireRequest{Op: "pty-kill", PtyKill: &req})
}

func RenamePty(ctx context.Context, req PtyRenameRequest) (PtySession, error) {
	return daemonCall[PtySession](ctx, wireRequest{Op: "pty-rename", PtyRename: &req})
}
