package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func clientMergeFrom(o client.Object) client.Patch { return client.MergeFrom(o) }

// rateLimiter allows n events per window per key (fixed window).
type rateLimiter struct {
	n      int
	window time.Duration
	mu     sync.Mutex
	seen   map[string]*bucket
}

type bucket struct {
	start time.Time
	count int
}

func newRateLimiter(n int, window time.Duration) *rateLimiter {
	return &rateLimiter{n: n, window: window, seen: map[string]*bucket{}}
}

// exceeded reports whether key reached the limit in the current window.
func (l *rateLimiter) exceeded(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.seen[key]
	return b != nil && time.Since(b.start) <= l.window && b.count >= l.n
}

// add counts an event.
func (l *rateLimiter) add(key string) { l.allow(key) }

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.seen) > 10000 {
		for k, b := range l.seen {
			if now.Sub(b.start) > l.window {
				delete(l.seen, k)
			}
		}
	}
	b := l.seen[key]
	if b == nil || now.Sub(b.start) > l.window {
		l.seen[key] = &bucket{start: now, count: 1}
		return true
	}
	b.count++
	return b.count <= l.n
}

// clientIP returns the client address, honoring the first X-Forwarded-For hop
// set by the ingress.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
