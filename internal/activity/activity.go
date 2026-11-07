// Package activity keeps a recent in-memory log of agent (MCP) tool calls per
// environment, shown in the console's agent activity panel.
//
// The log is not persisted: it covers the lifetime of the server process and
// is bounded per environment and globally.
package activity

import (
	"sync"
	"time"
)

// Entry is one tool call.
type Entry struct {
	ID          uint64    `json:"id"`
	Time        time.Time `json:"time"`
	Environment string    `json:"environment,omitempty"`
	User        string    `json:"user"`
	// Token is the name of the access token used.
	Token string `json:"token,omitempty"`
	Tool  string `json:"tool"`
	// Summary is a one line description (command, path, ...).
	Summary string `json:"summary,omitempty"`
	// Detail is optional longer text (output tail, diff), truncated.
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs"`
	// Running is true while the call is in progress.
	Running bool `json:"running,omitempty"`
}

const (
	perEnvironment = 500
	global         = 5000
	maxDetail      = 8 << 10
)

// Log stores entries and notifies subscribers.
type Log struct {
	mu     sync.Mutex
	nextID uint64
	all    []*Entry
	subs   map[chan Entry]struct{}
}

// Start records a call in progress and returns a function that completes it.
func (l *Log) Start(e Entry) func(summary, detail, errMsg string) {
	if l == nil {
		return func(string, string, string) {}
	}
	l.mu.Lock()
	l.nextID++
	e.ID = l.nextID
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Running = true
	ent := &e
	l.all = append(l.all, ent)
	l.trim()
	snap := *ent
	l.mu.Unlock()
	l.publish(snap)
	return func(summary, detail, errMsg string) {
		l.mu.Lock()
		if summary != "" {
			ent.Summary = summary
		}
		if len(detail) > maxDetail {
			detail = detail[:maxDetail] + "\n…"
		}
		ent.Detail = detail
		ent.Error = errMsg
		ent.Running = false
		ent.DurationMs = time.Since(ent.Time).Milliseconds()
		snap := *ent
		l.mu.Unlock()
		l.publish(snap)
	}
}

// trim drops the oldest entries over the limits. Caller holds mu.
func (l *Log) trim() {
	if len(l.all) > global {
		l.all = append([]*Entry(nil), l.all[len(l.all)-global:]...)
	}
	counts := map[string]int{}
	for _, e := range l.all {
		counts[e.Environment]++
	}
	if counts[l.all[len(l.all)-1].Environment] <= perEnvironment {
		return
	}
	env := l.all[len(l.all)-1].Environment
	out := l.all[:0]
	drop := counts[env] - perEnvironment
	for _, e := range l.all {
		if drop > 0 && e.Environment == env {
			drop--
			continue
		}
		out = append(out, e)
	}
	l.all = out
}

// List returns entries newest first, filtered by environment (empty = all)
// and by keep. At most limit entries are returned.
func (l *Log) List(environment string, limit int, keep func(*Entry) bool) []Entry {
	if l == nil {
		return []Entry{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Entry{}
	for i := len(l.all) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.all[i]
		if environment != "" && e.Environment != environment {
			continue
		}
		if keep != nil && !keep(e) {
			continue
		}
		out = append(out, *e)
	}
	return out
}

// Subscribe returns a channel of new and updated entries. Slow subscribers
// miss entries rather than block the log.
func (l *Log) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 64)
	l.mu.Lock()
	if l.subs == nil {
		l.subs = map[chan Entry]struct{}{}
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

func (l *Log) publish(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
