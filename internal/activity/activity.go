// Package activity records environment activity for idle auto-stop.
//
// Touches are coalesced in memory and flushed to status.lastActivityTime
// periodically, so frequent calls (exec, terminal keystrokes) do not each
// cause an API write. Status writes are merge patches of a single field,
// which do not conflict with the controller's status updates.
package activity

import (
	"context"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// Recorder implements manager.Runnable.
type Recorder struct {
	Client    client.Client
	Namespace string
	// Interval between flushes (default 30s).
	Interval time.Duration

	mu      sync.Mutex
	pending map[string]time.Time
}

// Touch marks the environment active now.
func (r *Recorder) Touch(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending == nil {
		r.pending = map[string]time.Time{}
	}
	r.pending[name] = time.Now()
}

// NeedLeaderElection implements manager.LeaderElectionRunnable: every replica
// records the activity it observes.
func (r *Recorder) NeedLeaderElection() bool { return false }

// Start flushes until the context ends.
func (r *Recorder) Start(ctx context.Context) error {
	interval := r.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush(context.Background())
			return nil
		case <-t.C:
			r.Flush(ctx)
		}
	}
}

// Flush writes pending activity.
func (r *Recorder) Flush(ctx context.Context) {
	r.mu.Lock()
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	for name, at := range pending {
		env := &devenvv1.DevEnvironment{}
		if err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: name}, env); err != nil {
			continue
		}
		if env.Status.LastActivityTime != nil && !env.Status.LastActivityTime.Time.Before(at.Truncate(time.Second)) {
			continue
		}
		patch := client.MergeFrom(env.DeepCopy())
		env.Status.LastActivityTime = &metav1.Time{Time: at}
		if err := r.Client.Status().Patch(ctx, env, patch); err != nil {
			logf.FromContext(ctx).V(1).Info("record activity failed", "environment", name, "error", err.Error())
		}
	}
}
