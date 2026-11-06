package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Background processes live in the environment under procDir/<id>:
//
//	cmd        the command line
//	cwd        working directory
//	started    start time (unix seconds)
//	pid        process group id (setsid leader)
//	output     combined stdout and stderr
//	exit_code  written when the process exits
//
// They are detached from the SSH session, so they keep running between tool
// calls. procDir is on /tmp, so state is lost when the environment restarts.
// procDir holds background process state; a variable for tests.
var procDir = "/tmp/devenv-proc"

const (
	defaultReadOutput  = 64 << 10
	maxReadOutputBytes = 256 << 10
	maxProcesses       = 32
)

var procIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

type startProcIn struct {
	Name        string            `json:"name" jsonschema:"environment name"`
	Command     string            `json:"command" jsonschema:"shell command, run with bash -lc as user dev"`
	ID          string            `json:"id,omitempty" jsonschema:"process id to refer to it later, e.g. dev-server (lowercase, digits, '-'); generated when empty"`
	Cwd         string            `json:"cwd,omitempty" jsonschema:"working directory (default /home/dev)"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	WaitSeconds int               `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds for the process to exit or wait_for to appear (default 0, max 50)"`
	WaitFor     string            `json:"wait_for,omitempty" jsonschema:"stop waiting when this text appears in the output, e.g. 'listening on'"`
}

type procInfo struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd,omitempty"`
	Running   bool      `json:"running"`
	ExitCode  *int      `json:"exit_code,omitempty" jsonschema:"set when the process has exited; -1 when killed"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at"`
	OutputLen int64     `json:"output_bytes" jsonschema:"total size of the output log"`
}

type startProcOut struct {
	Process procInfo `json:"process"`
	Output  string   `json:"output" jsonschema:"output so far (tail, up to 64KB)"`
}

type procIn struct {
	Name string `json:"name" jsonschema:"environment name"`
	ID   string `json:"id" jsonschema:"process id"`
}

type readProcIn struct {
	Name        string `json:"name" jsonschema:"environment name"`
	ID          string `json:"id" jsonschema:"process id"`
	Offset      *int64 `json:"offset,omitempty" jsonschema:"byte offset to read from (use next_offset from the previous call); default returns the tail"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum bytes to return (default 64KB, max 256KB)"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds for new output, process exit or wait_for (max 50)"`
	WaitFor     string `json:"wait_for,omitempty" jsonschema:"stop waiting when this text appears in new output"`
}

type readProcOut struct {
	Process    procInfo `json:"process"`
	Output     string   `json:"output"`
	Offset     int64    `json:"offset" jsonschema:"byte offset of output"`
	NextOffset int64    `json:"next_offset" jsonschema:"pass as offset to read only newer output"`
}

type listProcOut struct {
	Processes []procInfo `json:"processes"`
}

type stopProcIn struct {
	Name   string `json:"name" jsonschema:"environment name"`
	ID     string `json:"id" jsonschema:"process id"`
	Remove bool   `json:"remove,omitempty" jsonschema:"also delete its output log"`
}

// statusScript defines emit, which prints one tab separated line per
// process: id, pid, started, running, exit_code, output size, base64(cmd),
// base64(cwd). Free text is base64 encoded so no shell escaping is needed.
func statusScript() string {
	return `
d=` + procDir + `
emit() {
  p="$d/$1"; [ -d "$p" ] || return 0
  pid=$(cat "$p/pid" 2>/dev/null || echo 0)
  if [ -f "$p/exit_code" ]; then running=0; code=$(cat "$p/exit_code"); else
    if [ "$pid" -gt 0 ] && kill -0 "$pid" 2>/dev/null; then running=1; code=; else running=0; code=-1; fi
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$pid" "$(cat "$p/started" 2>/dev/null || echo 0)" "$running" "$code" \
    "$(stat -c %s "$p/output" 2>/dev/null || echo 0)" "$(base64 -w0 < "$p/cmd" 2>/dev/null)" "$(base64 -w0 < "$p/cwd" 2>/dev/null)"
}
`
}

func parseProcs(out string) ([]procInfo, error) {
	var list []procInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			return nil, errorf("unexpected process status line: %q", line)
		}
		pid, _ := strconv.Atoi(f[1])
		started, _ := strconv.ParseInt(f[2], 10, 64)
		size, _ := strconv.ParseInt(f[5], 10, 64)
		cmd, _ := base64.StdEncoding.DecodeString(f[6])
		cwd, _ := base64.StdEncoding.DecodeString(f[7])
		info := procInfo{ID: f[0], PID: pid, StartedAt: time.Unix(started, 0).UTC(), Running: f[3] == "1",
			OutputLen: size, Command: string(cmd), Cwd: string(cwd)}
		if f[4] != "" {
			code, err := strconv.Atoi(strings.TrimSpace(f[4]))
			if err != nil {
				code = -1
			}
			info.ExitCode = &code
		}
		list = append(list, info)
	}
	return list, nil
}

func checkID(id string) error {
	if !procIDRe.MatchString(id) {
		return errorf("invalid process id %q: lowercase letters, digits and '-', max 40", id)
	}
	return nil
}

func (s *Server) procStatus(ctx context.Context, name, id string) (procInfo, error) {
	res, err := s.runScript(ctx, name, statusScript()+"emit "+shellQuote(id), 30*time.Second)
	if err != nil {
		return procInfo{}, err
	}
	list, err := parseProcs(res.Stdout)
	if err != nil {
		return procInfo{}, err
	}
	if len(list) == 0 {
		return procInfo{}, errorf("process %q not found; list_processes shows existing processes (state is lost when the environment restarts)", id)
	}
	return list[0], nil
}

// readOutput reads output bytes [offset, offset+limit); offset < 0 reads the tail.
func (s *Server) readOutput(ctx context.Context, name, id string, offset int64, limit int) (string, int64, error) {
	p := procDir + "/" + id + "/output"
	var script string
	if offset < 0 {
		script = fmt.Sprintf(`s=$(stat -c %%s %[1]s 2>/dev/null || echo 0); o=$(( s > %[2]d ? s - %[2]d : 0 )); echo $o; tail -c +$((o+1)) %[1]s 2>/dev/null | head -c %[2]d`, shellQuote(p), limit)
	} else {
		script = fmt.Sprintf(`echo %[3]d; tail -c +%[4]d %[1]s 2>/dev/null | head -c %[2]d`, shellQuote(p), limit, offset, offset+1)
	}
	res, err := s.runScript(ctx, name, script, 30*time.Second)
	if err != nil {
		return "", 0, err
	}
	first, rest, _ := strings.Cut(res.Stdout, "\n")
	start, _ := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	return rest, start, nil
}

func (s *Server) startProcess(ctx context.Context, _ *mcp.CallToolRequest, in startProcIn) (*mcp.CallToolResult, startProcOut, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, startProcOut{}, errorf("command is required")
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("p%d", time.Now().UnixNano()%1_000_000_000)
	}
	if err := checkID(id); err != nil {
		return nil, startProcOut{}, err
	}
	cmd, err := buildCommand(execIn{Command: in.Command, Cwd: in.Cwd, Env: in.Env})
	if err != nil {
		return nil, startProcOut{}, err
	}
	// Run the user command as a child (not exec) so the wrapper can record
	// its exit code.
	inner := strings.TrimPrefix(cmd, "exec setsid ")
	cwd := in.Cwd
	if cwd == "" {
		cwd = homeDir
	}
	d := procDir + "/" + id
	// The wrapper records the exit code after the command ends; setsid
	// detaches it from the SSH session so it survives the call.
	// The wrapper writes its own PID (it is the session and process group
	// leader after setsid), so stop_process can signal the whole group.
	wrapper := fmt.Sprintf(`echo $$ > %s; %s; echo $? > %s`, shellQuote(d+"/pid"), inner, shellQuote(d+"/exit_code"))
	script := fmt.Sprintf(`set -e
mkdir -p %[1]s
n=$(ls %[1]s | wc -l); if [ "$n" -ge %[6]d ]; then echo "too many processes ($n); stop and remove old ones" >&2; exit 3; fi
p=%[2]s
if [ -f "$p/pid" ] && [ ! -f "$p/exit_code" ] && kill -0 "$(cat "$p/pid")" 2>/dev/null; then echo "process already running" >&2; exit 4; fi
rm -rf "$p"; mkdir -p "$p"
printf %%s %[3]s > "$p/cmd"; printf %%s %[4]s > "$p/cwd"; date +%%s > "$p/started"
setsid -f bash -c %[5]s > "$p/output" 2>&1 < /dev/null
for i in $(seq 50); do [ -s "$p/pid" ] && break; sleep 0.05; done
`, procDir, shellQuote(d), shellQuote(in.Command), shellQuote(cwd), shellQuote(wrapper), maxProcesses)
	res, err := s.runScript(ctx, in.Name, script, 30*time.Second)
	if err != nil {
		return nil, startProcOut{}, err
	}
	switch res.ExitCode {
	case 0:
	case 3, 4:
		return nil, startProcOut{}, errorf("%s", strings.TrimSpace(res.Stderr))
	default:
		return nil, startProcOut{}, errorf("start process failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var out readProcOut
	if err := s.waitProcess(ctx, in.Name, id, -1, defaultReadOutput, in.WaitSeconds, in.WaitFor, &out, 0); err != nil {
		return nil, startProcOut{}, err
	}
	return nil, startProcOut{Process: out.Process, Output: out.Output}, nil
}

// waitProcess polls status and output until the process exits, waitFor
// appears, new output arrives (when requireNew), or the wait ends.
func (s *Server) waitProcess(ctx context.Context, name, id string, offset int64, limit, waitSec int, waitFor string, out *readProcOut, minNew int64) error {
	wait := time.Duration(min(max(waitSec, 0), 50)) * time.Second
	deadline := time.Now().Add(wait)
	for {
		info, err := s.procStatus(ctx, name, id)
		if err != nil {
			return err
		}
		text, start, err := s.readOutput(ctx, name, id, offset, limit)
		if err != nil {
			return err
		}
		*out = readProcOut{Process: info, Output: text, Offset: start, NextOffset: start + int64(len(text))}
		done := !info.Running || time.Now().After(deadline) ||
			(waitFor != "" && strings.Contains(text, waitFor)) ||
			(waitFor == "" && minNew > 0 && int64(len(text)) >= minNew)
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) readProcessOutput(ctx context.Context, _ *mcp.CallToolRequest, in readProcIn) (*mcp.CallToolResult, readProcOut, error) {
	if err := checkID(in.ID); err != nil {
		return nil, readProcOut{}, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultReadOutput
	}
	limit = min(limit, maxReadOutputBytes)
	offset := int64(-1)
	if in.Offset != nil {
		offset = max(*in.Offset, 0)
	}
	var out readProcOut
	// When following (offset given) wait for at least one new byte.
	minNew := int64(0)
	if in.Offset != nil {
		minNew = 1
	}
	if err := s.waitProcess(ctx, in.Name, in.ID, offset, limit, in.WaitSeconds, in.WaitFor, &out, minNew); err != nil {
		return nil, readProcOut{}, err
	}
	return nil, out, nil
}

func (s *Server) listProcesses(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, listProcOut, error) {
	res, err := s.runScript(ctx, in.Name, statusScript()+`for p in $(ls `+procDir+` 2>/dev/null); do emit "$p"; done`, 30*time.Second)
	if err != nil {
		return nil, listProcOut{}, err
	}
	list, err := parseProcs(res.Stdout)
	if err != nil {
		return nil, listProcOut{}, err
	}
	if list == nil {
		list = []procInfo{}
	}
	return nil, listProcOut{Processes: list}, nil
}

func (s *Server) stopProcess(ctx context.Context, _ *mcp.CallToolRequest, in stopProcIn) (*mcp.CallToolResult, startProcOut, error) {
	if err := checkID(in.ID); err != nil {
		return nil, startProcOut{}, err
	}
	d := shellQuote(procDir + "/" + in.ID)
	script := fmt.Sprintf(`p=%s; [ -d "$p" ] || exit 5
pid=$(cat "$p/pid" 2>/dev/null || echo 0)
if [ ! -f "$p/exit_code" ] && [ "$pid" -gt 0 ]; then
  kill -TERM -- -"$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null
  for i in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  kill -KILL -- -"$pid" 2>/dev/null || true
  [ -f "$p/exit_code" ] || echo -1 > "$p/exit_code"
fi`, d)
	res, err := s.runScript(ctx, in.Name, script, 30*time.Second)
	if err != nil {
		return nil, startProcOut{}, err
	}
	if res.ExitCode == 5 {
		return nil, startProcOut{}, errorf("process %q not found", in.ID)
	}
	info, err := s.procStatus(ctx, in.Name, in.ID)
	if err != nil {
		return nil, startProcOut{}, err
	}
	text, _, _ := s.readOutput(ctx, in.Name, in.ID, -1, 8<<10)
	if in.Remove {
		if _, err := s.runScript(ctx, in.Name, "rm -rf "+d, 30*time.Second); err != nil {
			return nil, startProcOut{}, err
		}
	}
	return nil, startProcOut{Process: info, Output: text}, nil
}

func (s *Server) registerProcesses(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "start_process",
		Description: "Start a long running command in the background (dev servers, watchers, long builds or test runs) and return immediately or after wait_seconds. " +
			"Output (stdout+stderr) is kept in a log; follow it with read_process_output. Processes keep running between calls but end when the environment stops.",
	}, logged("start_process", s.startProcess))
	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_process_output",
		Description: "Read a background process's output and status. Without offset returns the latest output; pass next_offset from the previous call to read only new output. " +
			"wait_seconds waits for new output, exit or wait_for text (max 50s per call; call again to keep waiting).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, logged("read_process_output", s.readProcessOutput))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_processes",
		Description: "List background processes started with start_process.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, logged("list_processes", s.listProcesses))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "stop_process",
		Description: "Stop a background process and everything it started (SIGTERM, then SIGKILL after 5s). Returns its final output.",
	}, logged("stop_process", s.stopProcess))
}
