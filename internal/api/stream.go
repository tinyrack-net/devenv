package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// Broker fans out environment changes from the database to SSE clients.
type Broker struct {
	mu   sync.Mutex
	subs map[chan envEvent]struct{}
}

type envEvent struct {
	Type string // "upsert" or "delete"
	Name string
	Env  *model.Environment // nil for deletes
}

// Watch follows database changes (this process and, through polling, other
// writers) until ctx ends.
func (b *Broker) Watch(ctx context.Context, db *store.Store) {
	changes, cancel := db.Subscribe()
	go func() {
		<-ctx.Done()
		cancel()
	}()
	go db.Poll(ctx, 2*time.Second)
	go func() {
		for c := range changes {
			if c.Kind != store.KindEnvironment {
				continue
			}
			env, err := db.GetEnvironment(ctx, c.Name)
			switch {
			case errors.Is(err, store.ErrNotFound):
				b.publish(envEvent{Type: "delete", Name: c.Name})
			case err == nil:
				b.publish(envEvent{Type: "upsert", Name: c.Name, Env: env})
			}
		}
	}()
}

func (b *Broker) publish(ev envEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *Broker) subscribe() (chan envEvent, func()) {
	ch := make(chan envEvent, 128)
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[chan envEvent]struct{}{}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// stream is a Server-Sent Events endpoint:
//
//	event: environment  data: {"type":"upsert","environment":{...}}
//	event: environment  data: {"type":"delete","name":"x"}
//	event: activity     data: {...activity.Entry}
//
// Clients reload their lists on (re)connect and apply events afterwards.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	p := principal(r)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()

	var envs chan envEvent
	if s.Events != nil {
		var cancel func()
		envs, cancel = s.Events.subscribe()
		defer cancel()
	}
	var acts <-chan activity.Entry
	if s.Activity != nil {
		var cancel func()
		acts, cancel = s.Activity.Subscribe()
		defer cancel()
	}
	send := func(event string, v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	// Access to an environment can change with its spec; remember what this
	// client saw so it gets a delete when access is lost.
	visible := map[string]bool{}
	if list, err := s.DB.ListEnvironments(r.Context()); err == nil {
		for i := range list {
			if access.Environment(p, &list[i]) != access.None {
				visible[list[i].Name] = true
			}
		}
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case ev := <-envs:
			lvl := access.None
			if ev.Env != nil {
				lvl = access.Environment(p, ev.Env)
			}
			if ev.Env == nil || lvl == access.None {
				if visible[ev.Name] {
					delete(visible, ev.Name)
					if !send("environment", map[string]any{"type": "delete", "name": ev.Name}) {
						return
					}
				}
				continue
			}
			visible[ev.Name] = true
			if !send("environment", map[string]any{"type": "upsert", "environment": s.view(ev.Env, lvl)}) {
				return
			}
		case e := <-acts:
			if !p.Admin && e.User != p.Name && !visible[e.Environment] {
				continue
			}
			if !send("activity", e) {
				return
			}
		}
	}
}
