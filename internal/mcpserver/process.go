package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tinyrack-net/devenv/internal/agent"
)

// Background processes are owned by the devenv-agent daemon inside the
// environment. Each tool call is one helper request; waiting happens in the
// daemon (no polling over SSH).
const (
	defaultReadOutput  = 64 << 10
	maxReadOutputBytes = 256 << 10
	maxWait            = 50 * time.Second
)

type startProcIn struct {
	Name        string            `json:"name" jsonschema:"environment name"`
	Command     string            `json:"command" jsonschema:"shell command, run with bash -lc as user dev"`
	ID          string            `json:"id,omitempty" jsonschema:"process id to refer to it later, e.g. dev-server (lowercase, digits, '-'); generated when empty"`
	Cwd         string            `json:"cwd,omitempty" jsonschema:"working directory (default /home/dev)"`
	Env         map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	WaitSeconds int               `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds for the process to exit or wait_for to appear (default 0, max 50)"`
	WaitFor     string            `json:"wait_for,omitempty" jsonschema:"stop waiting when this text appears in the output, e.g. 'listening on'"`
}

type readProcIn struct {
	Name        string `json:"name" jsonschema:"environment name"`
	ID          string `json:"id" jsonschema:"process id"`
	Offset      *int64 `json:"offset,omitempty" jsonschema:"byte offset to read from (use next_offset from the previous call); default returns the latest output"`
	Limit       int    `json:"limit,omitempty" jsonschema:"maximum bytes to return (default 64KB, max 256KB)"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds for new output, exit or wait_for (max 50)"`
	WaitFor     string `json:"wait_for,omitempty" jsonschema:"stop waiting when this text appears in the output"`
}

type procIn struct {
	Name string `json:"name" jsonschema:"environment name"`
	ID   string `json:"id" jsonschema:"process id"`
}

type stopProcIn struct {
	Name   string `json:"name" jsonschema:"environment name"`
	ID     string `json:"id" jsonschema:"process id"`
	Remove bool   `json:"remove,omitempty" jsonschema:"also forget it and its output"`
}

func waitMs(sec int) int64 {
	return min(time.Duration(max(sec, 0))*time.Second, maxWait).Milliseconds()
}

func readLimit(n int) int {
	if n <= 0 {
		return defaultReadOutput
	}
	return min(n, maxReadOutputBytes)
}

func (s *Server) startProcess(ctx context.Context, _ *mcp.CallToolRequest, in startProcIn) (*mcp.CallToolResult, agent.Output, error) {
	if in.Command == "" {
		return nil, agent.Output{}, errorf("command is required")
	}
	id := in.ID
	if id == "" {
		id = fmt.Sprintf("p%d", time.Now().UnixNano()%1_000_000_000)
	}
	cwd := in.Cwd
	if cwd == "" {
		cwd = homeDir
	}
	out, err := helper[agent.Output](s, ctx, in.Name, "start", agent.StartRequest{
		ID: id, Command: in.Command, Cwd: cwd, Env: in.Env,
		WaitMs: waitMs(in.WaitSeconds), WaitFor: in.WaitFor, Limit: defaultReadOutput,
	}, maxWait+30*time.Second)
	return nil, out, err
}

func (s *Server) readProcessOutput(ctx context.Context, _ *mcp.CallToolRequest, in readProcIn) (*mcp.CallToolResult, agent.Output, error) {
	offset := int64(-1)
	if in.Offset != nil {
		offset = max(*in.Offset, 0)
	}
	out, err := helper[agent.Output](s, ctx, in.Name, "read", agent.ReadRequest{
		ID: in.ID, Offset: offset, Limit: readLimit(in.Limit), WaitMs: waitMs(in.WaitSeconds), WaitFor: in.WaitFor,
	}, maxWait+30*time.Second)
	return nil, out, err
}

func (s *Server) listProcesses(ctx context.Context, _ *mcp.CallToolRequest, in nameIn) (*mcp.CallToolResult, agent.ProcessList, error) {
	out, err := helper[agent.ProcessList](s, ctx, in.Name, "list", struct{}{}, 30*time.Second)
	return nil, out, err
}

func (s *Server) stopProcess(ctx context.Context, _ *mcp.CallToolRequest, in stopProcIn) (*mcp.CallToolResult, agent.Output, error) {
	out, err := helper[agent.Output](s, ctx, in.Name, "stop", agent.StopRequest{ID: in.ID, Remove: in.Remove}, 40*time.Second)
	return nil, out, err
}

func (s *Server) registerProcesses(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "start_process",
		Description: "Start a long running command in the background (dev servers, watchers, long builds or test runs) and return immediately or after wait_seconds. " +
			"Output (stdout+stderr, last 8MB kept) can be followed with read_process_output. Processes keep running between calls but end when the environment stops.",
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
