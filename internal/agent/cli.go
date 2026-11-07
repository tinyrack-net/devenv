package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Main is the entry point of the devenv-agent binary.
//
//	devenv-agent <op>      read a JSON request from stdin, write a JSON response
//	devenv-agent daemon    run the background process daemon
func Main(args []string, build string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: devenv-agent <exec|search|find|stats|start|read|stop|list|pty-attach|pty-list|pty-kill|pty-rename|ports|version|daemon>")
		return 2
	}
	op := args[0]
	if op == "sleep" {
		// Placeholder container of the image prepull DaemonSet: wait for
		// SIGTERM (a bare select{} is reported as a deadlock by the runtime).
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		return 0
	}
	if op == "pty-attach" {
		// Streams: the first stdin line is the daemon request
		// ({"op":"pty-attach","pty_attach":{...}}), then frames.
		if err := PtyAttach(context.Background(), os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "pty-attach:", err)
			return 1
		}
		return 0
	}
	if op == "daemon" {
		if err := RunDaemon(10 * time.Minute); err != nil {
			fmt.Fprintln(os.Stderr, "daemon:", err)
			return 1
		}
		return 0
	}
	// The server bounds each request; this is a safety net.
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()

	in, err := io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
	if err != nil {
		return reply[any](nil, err)
	}
	switch op {
	case "version":
		return reply(VersionResult{Version: Version, Build: build}, nil)
	case "exec":
		var req ExecRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(Exec(ctx, req))
	case "search":
		var req SearchRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(Search(req))
	case "find":
		var req FindRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(Find(req))
	case "stats":
		var req StatsRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(Stats(req))
	case "start":
		var req StartRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(StartProcess(ctx, req))
	case "read":
		var req ReadRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(ReadProcess(ctx, req))
	case "stop":
		var req StopRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(StopProcess(ctx, req))
	case "list":
		return reply(ListProcesses(ctx))
	case "pty-list":
		return reply(ListPty(ctx))
	case "pty-kill":
		var req PtyKillRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(KillPty(ctx, req))
	case "pty-rename":
		var req PtyRenameRequest
		if err := decode(in, &req); err != nil {
			return reply[any](nil, err)
		}
		return reply(RenamePty(ctx, req))
	case "ports":
		return reply(ListPorts())
	}
	return reply[any](nil, fmt.Errorf("unknown op %q", op))
}

func decode(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func reply[T any](v T, err error) int {
	writeResp(os.Stdout, v, err)
	return 0
}
