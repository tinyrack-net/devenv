// Package agent implements devenv-agent, the helper binary mounted into every
// environment (read-only image volume at /opt/devenv). The devenv server runs
// it over SSH as the dev user:
//
//	/opt/devenv/bin/devenv-agent <op>     JSON request on stdin, JSON response on stdout
//
// One SSH session per request keeps the helper stateless; background
// processes are owned by a small daemon the helper starts on demand
// (devenv-agent daemon, unix socket under $XDG_RUNTIME_DIR or /tmp).
//
// The helper exists for what can not be done correctly or efficiently from
// outside over plain SSH: process groups, exact exit codes and kills, OOM
// detection, and walking/grepping file trees without shipping file contents.
package agent

import "time"

// Path is where the helper is mounted in environments.
const Path = "/opt/devenv/bin/devenv-agent"

// Version is bumped when the protocol changes incompatibly.
const Version = 1

// Response wraps every reply.
type Response[T any] struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Result T      `json:"result,omitempty"`
}

// ---- exec

type ExecRequest struct {
	Command   string            `json:"command"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeout_ms"`
	// MaxOutput is kept per stream (head and tail).
	MaxOutput int `json:"max_output"`
}

type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Signal     string `json:"signal,omitempty"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	TimedOut   bool   `json:"timed_out"`
	Truncated  bool   `json:"truncated"`
	OOMKilled  bool   `json:"oom_killed"`
	DurationMs int64  `json:"duration_ms"`
}

// ---- background processes

type StartRequest struct {
	ID      string            `json:"id"`
	Command string            `json:"command"`
	Cwd     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Wait up to WaitMs for exit or WaitFor in the output.
	WaitMs  int64  `json:"wait_ms,omitempty"`
	WaitFor string `json:"wait_for,omitempty"`
	Limit   int    `json:"limit"`
}

type ReadRequest struct {
	ID string `json:"id"`
	// Offset < 0 returns the tail.
	Offset  int64  `json:"offset"`
	Limit   int    `json:"limit"`
	WaitMs  int64  `json:"wait_ms,omitempty"`
	WaitFor string `json:"wait_for,omitempty"`
}

type StopRequest struct {
	ID     string `json:"id"`
	Remove bool   `json:"remove,omitempty"`
}

type Process struct {
	ID        string    `json:"id"`
	Command   string    `json:"command"`
	Cwd       string    `json:"cwd,omitempty"`
	PID       int       `json:"pid"`
	Running   bool      `json:"running"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Signal    string    `json:"signal,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	OutputLen int64     `json:"output_bytes"`
}

type Output struct {
	Process    Process `json:"process"`
	Output     string  `json:"output"`
	Offset     int64   `json:"offset"`
	NextOffset int64   `json:"next_offset"`
	// Dropped is set when the requested offset was older than the kept log.
	Dropped bool `json:"dropped,omitempty"`
}

type ProcessList struct {
	Processes []Process `json:"processes"`
}

// ---- search

type SearchRequest struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path"`
	Glob           string `json:"glob,omitempty"`
	IgnoreCase     bool   `json:"ignore_case,omitempty"`
	FixedStrings   bool   `json:"fixed_strings,omitempty"`
	Context        int    `json:"context,omitempty"`
	FilesOnly      bool   `json:"files_only,omitempty"`
	MaxResults     int    `json:"max_results"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
}

type SearchResult struct {
	Results   string `json:"results"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	// FilesScanned helps agents see how broad a search was.
	FilesScanned int `json:"files_scanned"`
}

type FindRequest struct {
	Pattern        string `json:"pattern,omitempty"`
	Path           string `json:"path"`
	MaxResults     int    `json:"max_results"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
}

type FindResult struct {
	Files     []string `json:"files"`
	Truncated bool     `json:"truncated"`
}

// ---- stats

type StatsRequest struct {
	Path string `json:"path"`
}

type StatsResult struct {
	DiskTotal   uint64 `json:"disk_total"`
	DiskUsed    uint64 `json:"disk_used"`
	MemoryUsed  uint64 `json:"memory_used,omitempty"`
	MemoryLimit uint64 `json:"memory_limit,omitempty"`
	OOMKills    uint64 `json:"oom_kills"`
	Processes   int    `json:"processes"`
}

// ---- version

type VersionResult struct {
	Version int    `json:"version"`
	Build   string `json:"build"`
}
