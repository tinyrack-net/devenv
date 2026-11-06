// Package server runs the HTTP API as a controller-runtime Runnable.
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// HTTP is a manager Runnable serving a handler. It runs on every replica,
// not only the leader.
type HTTP struct {
	Addr    string
	Handler http.Handler
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (h *HTTP) NeedLeaderElection() bool { return false }

// Start implements manager.Runnable.
func (h *HTTP) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("http")
	srv := &http.Server{
		Addr:              h.Addr,
		Handler:           h.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", h.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
