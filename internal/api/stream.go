package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/activity"
)

// Broker fans out environment changes from the informer cache to SSE clients.
type Broker struct {
	mu   sync.Mutex
	subs map[chan envEvent]struct{}
}

type envEvent struct {
	Type string // "upsert" or "delete"
	Env  *devenvv1.DevEnvironment
}

// Watch registers informer handlers. Call before the cache starts or after;
// the informer replays existing objects as adds.
func (b *Broker) Watch(ctx context.Context, c cache.Cache) error {
	inf, err := c.GetInformer(ctx, &devenvv1.DevEnvironment{})
	if err != nil {
		return err
	}
	_, err = inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(o any) { b.publish("upsert", o) },
		UpdateFunc: func(_, o any) { b.publish("upsert", o) },
		DeleteFunc: func(o any) {
			if t, ok := o.(toolscache.DeletedFinalStateUnknown); ok {
				o = t.Obj
			}
			b.publish("delete", o)
		},
	})
	return err
}

func (b *Broker) publish(typ string, o any) {
	env, ok := o.(*devenvv1.DevEnvironment)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- envEvent{Type: typ, Env: env}:
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
			name := ev.Env.Name
			lvl := access.Environment(p, ev.Env)
			if ev.Type == "delete" || lvl == access.None {
				if visible[name] || (ev.Type == "delete" && lvl != access.None) {
					delete(visible, name)
					if !send("environment", map[string]any{"type": "delete", "name": name}) {
						return
					}
				}
				continue
			}
			visible[name] = true
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
