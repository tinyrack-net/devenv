package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Background processes are owned by a per-user daemon so they survive the
// SSH session that started them and keep exact exit status. The daemon
// listens on a unix socket; clients start it on demand.
const (
	maxProcesses = 32
	// maxLogBytes is how much output is kept per process (oldest dropped).
	maxLogBytes = 8 << 20
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func stateDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("devenv-agent-%d", os.Getuid()))
}

func socketPath() string { return filepath.Join(stateDir(), "sock") }

// ---- daemon side

type proc struct {
	mu      sync.Mutex
	info    Process
	log     *ringLog
	cmd     *exec.Cmd
	done    chan struct{}
	changed *sync.Cond
}

// ringLog keeps the last maxLogBytes of output and the absolute offset of
// its first byte.
type ringLog struct {
	buf   []byte
	start int64 // absolute offset of buf[0]
}

func (r *ringLog) Write(p []byte) {
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - maxLogBytes; over > 0 {
		r.buf = append([]byte(nil), r.buf[over:]...)
		r.start += int64(over)
	}
}

func (r *ringLog) end() int64 { return r.start + int64(len(r.buf)) }

// read returns bytes [offset, offset+limit) or the tail when offset < 0.
func (r *ringLog) read(offset int64, limit int) (string, int64, bool) {
	dropped := false
	if offset < 0 {
		offset = max(r.end()-int64(limit), r.start)
	}
	if offset < r.start {
		offset, dropped = r.start, true
	}
	if offset > r.end() {
		offset = r.end()
	}
	from := offset - r.start
	to := min(from+int64(limit), int64(len(r.buf)))
	return string(r.buf[from:to]), offset, dropped
}

type daemon struct {
	mu    sync.Mutex
	procs map[string]*proc
	ptys  ptyRegistry
	// lastUse lets an idle daemon with no processes exit.
	lastUse time.Time
}

type wireRequest struct {
	Op        string            `json:"op"`
	Start     *StartRequest     `json:"start,omitempty"`
	Read      *ReadRequest      `json:"read,omitempty"`
	Stop      *StopRequest      `json:"stop,omitempty"`
	PtyAttach *PtyAttachRequest `json:"pty_attach,omitempty"`
	PtyKill   *PtyKillRequest   `json:"pty_kill,omitempty"`
	PtyRename *PtyRenameRequest `json:"pty_rename,omitempty"`
}

// RunDaemon serves until idle for idleExit with no running processes.
func RunDaemon(idleExit time.Duration) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	sock := socketPath()
	// Refuse to run twice.
	if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
		c.Close()
		return errors.New("daemon already running")
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer ln.Close()
	_ = os.Chmod(sock, 0o600)

	d := &daemon{procs: map[string]*proc{}, lastUse: time.Now()}
	go func() {
		for range time.Tick(30 * time.Second) {
			if d.idle(idleExit) {
				ln.Close()
				return
			}
		}
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil
		}
		go d.serve(c)
	}
}

func (d *daemon) idle(after time.Duration) bool {
	if d.ptys.count() > 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.procs {
		p.mu.Lock()
		running := p.info.Running
		p.mu.Unlock()
		if running {
			return false
		}
	}
	return time.Since(d.lastUse) > after
}

func (d *daemon) serve(c net.Conn) {
	defer c.Close()
	d.mu.Lock()
	d.lastUse = time.Now()
	d.mu.Unlock()
	// One JSON line, then (for pty-attach) a framed stream.
	rd := bufio.NewReader(c)
	line, err := rd.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		writeResp[any](c, nil, err)
		return
	}
	var req wireRequest
	if err := json.Unmarshal(line, &req); err != nil {
		writeResp[any](c, nil, err)
		return
	}
	switch req.Op {
	case "pty-attach":
		if req.PtyAttach == nil {
			writeResp[any](c, nil, errors.New("missing pty_attach"))
			return
		}
		d.ptys.attach(c, rd, *req.PtyAttach)
	case "pty-list":
		writeResp(c, d.ptys.list(), nil)
	case "pty-kill":
		if req.PtyKill == nil {
			writeResp[any](c, nil, errors.New("missing pty_kill"))
			return
		}
		writeResp[any](c, nil, d.ptys.kill(req.PtyKill.ID))
	case "pty-rename":
		if req.PtyRename == nil {
			writeResp[any](c, nil, errors.New("missing pty_rename"))
			return
		}
		out, err := d.ptys.rename(*req.PtyRename)
		writeResp(c, out, err)
	case "start":
		out, err := d.start(*req.Start)
		writeResp(c, out, err)
	case "read":
		out, err := d.read(*req.Read)
		writeResp(c, out, err)
	case "stop":
		out, err := d.stop(*req.Stop)
		writeResp(c, out, err)
	case "list":
		writeResp(c, d.list(), nil)
	case "ping":
		writeResp(c, "pong", nil)
	default:
		writeResp[any](c, nil, fmt.Errorf("unknown op %q", req.Op))
	}
}

func writeResp[T any](w io.Writer, v T, err error) {
	r := Response[T]{OK: err == nil, Result: v}
	if err != nil {
		r.Error = err.Error()
	}
	_ = json.NewEncoder(w).Encode(r)
}

func (d *daemon) start(req StartRequest) (Output, error) {
	if !idRe.MatchString(req.ID) {
		return Output{}, fmt.Errorf("invalid process id %q: lowercase letters, digits and '-', max 40", req.ID)
	}
	d.mu.Lock()
	if p, ok := d.procs[req.ID]; ok {
		p.mu.Lock()
		running := p.info.Running
		p.mu.Unlock()
		if running {
			d.mu.Unlock()
			return Output{}, fmt.Errorf("process %q is already running; stop it first or use another id", req.ID)
		}
	} else if len(d.procs) >= maxProcesses {
		d.mu.Unlock()
		return Output{}, fmt.Errorf("too many processes (%d); stop and remove old ones", len(d.procs))
	}
	cmd, err := shellCommand(req.Command, req.Cwd, req.Env)
	if err != nil {
		d.mu.Unlock()
		return Output{}, err
	}
	// Output goes to a pipe, so programs block-buffer it and wait_for or
	// read_process_output see nothing for a long time. Ask the common
	// runtimes to flush line by line (callers can override in env).
	for k, v := range map[string]string{"PYTHONUNBUFFERED": "1", "NODE_DISABLE_COLORS": "1"} {
		if _, ok := req.Env[k]; !ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	p := &proc{log: &ringLog{}, cmd: cmd, done: make(chan struct{})}
	p.changed = sync.NewCond(&p.mu)
	w := &procWriter{p: p}
	cmd.Stdout, cmd.Stderr = w, w
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		d.mu.Unlock()
		return Output{}, err
	}
	p.info = Process{ID: req.ID, Command: req.Command, Cwd: cmd.Dir, PID: cmd.Process.Pid, Running: true, StartedAt: time.Now().UTC()}
	d.procs[req.ID] = p
	d.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		code, sig := exitInfo(cmd.ProcessState)
		killGroup(cmd.Process.Pid, syscall.SIGKILL) // leftovers in the group
		p.mu.Lock()
		p.info.Running = false
		p.info.ExitCode = &code
		p.info.Signal = sig
		p.info.EndedAt = time.Now().UTC()
		p.changed.Broadcast()
		p.mu.Unlock()
		close(p.done)
	}()
	return d.read(ReadRequest{ID: req.ID, Offset: -1, Limit: req.Limit, WaitMs: req.WaitMs, WaitFor: req.WaitFor})
}

type procWriter struct{ p *proc }

func (w *procWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	w.p.log.Write(b)
	w.p.info.OutputLen = w.p.log.end()
	w.p.changed.Broadcast()
	w.p.mu.Unlock()
	return len(b), nil
}

func (d *daemon) get(id string) (*proc, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.procs[id]
	if !ok {
		return nil, fmt.Errorf("process %q not found; list_processes shows existing ones (they are lost when the environment restarts)", id)
	}
	return p, nil
}

// read waits (bounded) for exit, wait_for text, or new output when following.
func (d *daemon) read(req ReadRequest) (Output, error) {
	p, err := d.get(req.ID)
	if err != nil {
		return Output{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 64 << 10
	}
	deadline := time.Now().Add(time.Duration(req.WaitMs) * time.Millisecond)
	// Wake waiters at the deadline.
	if req.WaitMs > 0 {
		t := time.AfterFunc(time.Until(deadline), func() {
			p.mu.Lock()
			p.changed.Broadcast()
			p.mu.Unlock()
		})
		defer t.Stop()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		text, off, dropped := p.log.read(req.Offset, limit)
		satisfied := !p.info.Running || time.Now().After(deadline) ||
			(req.WaitFor != "" && strings.Contains(text, req.WaitFor)) ||
			(req.WaitFor == "" && req.Offset >= 0 && len(text) > 0) ||
			(req.WaitFor == "" && req.Offset < 0 && req.WaitMs == 0)
		if satisfied {
			return Output{Process: p.info, Output: text, Offset: off, NextOffset: off + int64(len(text)), Dropped: dropped}, nil
		}
		p.changed.Wait()
	}
}

func (d *daemon) stop(req StopRequest) (Output, error) {
	p, err := d.get(req.ID)
	if err != nil {
		return Output{}, err
	}
	p.mu.Lock()
	running, pid := p.info.Running, p.info.PID
	p.mu.Unlock()
	if running {
		killGroup(pid, syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			killGroup(pid, syscall.SIGKILL)
			<-p.done
		}
	}
	out, err := d.read(ReadRequest{ID: req.ID, Offset: -1, Limit: 8 << 10})
	if req.Remove {
		d.mu.Lock()
		delete(d.procs, req.ID)
		d.mu.Unlock()
	}
	return out, err
}

func (d *daemon) list() ProcessList {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := ProcessList{Processes: []Process{}}
	for _, p := range d.procs {
		p.mu.Lock()
		out.Processes = append(out.Processes, p.info)
		p.mu.Unlock()
	}
	sort.Slice(out.Processes, func(i, j int) bool { return out.Processes[i].StartedAt.Before(out.Processes[j].StartedAt) })
	return out
}

// ---- client side

// daemonCall sends one request, starting the daemon when needed.
func daemonCall[T any](ctx context.Context, req wireRequest) (T, error) {
	var zero T
	c, err := dialDaemon(ctx)
	if err != nil {
		return zero, err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return zero, err
	}
	var resp Response[T]
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&resp); err != nil {
		return zero, fmt.Errorf("daemon response: %w", err)
	}
	if !resp.OK {
		return zero, errors.New(resp.Error)
	}
	return resp.Result, nil
}

func dialDaemon(ctx context.Context) (net.Conn, error) {
	if c, err := net.DialTimeout("unix", socketPath(), time.Second); err == nil {
		return c, nil
	}
	if err := spawnDaemon(); err != nil {
		return nil, err
	}
	var lastErr error
	for i := 0; i < 50; i++ {
		c, err := net.DialTimeout("unix", socketPath(), time.Second)
		if err == nil {
			return c, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("start background process daemon: %w", lastErr)
}

// spawnDaemon starts "devenv-agent daemon" detached from the SSH session.
func spawnDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(stateDir(), "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(self, "daemon")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func StartProcess(ctx context.Context, req StartRequest) (Output, error) {
	return daemonCall[Output](ctx, wireRequest{Op: "start", Start: &req})
}

func ReadProcess(ctx context.Context, req ReadRequest) (Output, error) {
	return daemonCall[Output](ctx, wireRequest{Op: "read", Read: &req})
}

func StopProcess(ctx context.Context, req StopRequest) (Output, error) {
	return daemonCall[Output](ctx, wireRequest{Op: "stop", Stop: &req})
}

func ListProcesses(ctx context.Context) (ProcessList, error) {
	return daemonCall[ProcessList](ctx, wireRequest{Op: "list"})
}
