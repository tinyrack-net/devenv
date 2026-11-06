package sshclient

import (
	"context"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Pool keeps one SSH connection per environment and reuses it for many
// sessions (a new session costs ~25ms vs ~60ms for a new connection). Idle
// connections are closed; broken ones are dropped and redialed.
type Pool struct {
	Dialer *Dialer
	// IdleTimeout closes connections unused for this long (default 5m).
	IdleTimeout time.Duration

	mu    sync.Mutex
	conns map[string]*pooled
	once  sync.Once
}

type pooled struct {
	client   *ssh.Client
	lastUsed time.Time
	inUse    int
}

// Lease is a borrowed connection. Release it when done; call Fail when the
// connection misbehaved so it is not reused.
type Lease struct {
	Client *ssh.Client
	pool   *Pool
	name   string
	p      *pooled
}

func (p *Pool) start() {
	p.once.Do(func() {
		p.conns = map[string]*pooled{}
		go p.reaper()
	})
}

func (p *Pool) idle() time.Duration {
	if p.IdleTimeout > 0 {
		return p.IdleTimeout
	}
	return 5 * time.Minute
}

// Get returns a live connection to the environment, dialing when needed.
func (p *Pool) Get(ctx context.Context, name string) (*Lease, error) {
	p.start()
	p.mu.Lock()
	if c := p.conns[name]; c != nil {
		// Cheap liveness check: a keepalive round trip.
		c.inUse++
		p.mu.Unlock()
		if _, _, err := c.client.SendRequest("keepalive@devenv", true, nil); err == nil {
			return &Lease{Client: c.client, pool: p, name: name, p: c}, nil
		}
		p.mu.Lock()
		c.inUse--
		p.mu.Unlock()
		p.drop(name, c)
	} else {
		p.mu.Unlock()
	}

	client, err := p.Dialer.Dial(ctx, name)
	if err != nil {
		return nil, err
	}
	c := &pooled{client: client, lastUsed: time.Now(), inUse: 1}
	p.mu.Lock()
	if old := p.conns[name]; old != nil && old.inUse == 0 {
		old.client.Close()
	}
	p.conns[name] = c
	p.mu.Unlock()
	return &Lease{Client: client, pool: p, name: name, p: c}, nil
}

// Release returns the connection to the pool.
func (l *Lease) Release() {
	l.pool.mu.Lock()
	defer l.pool.mu.Unlock()
	l.p.inUse--
	l.p.lastUsed = time.Now()
	if l.pool.conns[l.name] != l.p && l.p.inUse <= 0 {
		l.p.client.Close() // replaced while leased
	}
}

// Fail drops the connection (e.g. after a transport error).
func (l *Lease) Fail() {
	l.pool.drop(l.name, l.p)
	l.Release()
}

func (p *Pool) drop(name string, c *pooled) {
	p.mu.Lock()
	if p.conns[name] == c {
		delete(p.conns, name)
	}
	p.mu.Unlock()
	c.client.Close()
}

// Forget closes the connection of an environment (restart, delete).
func (p *Pool) Forget(name string) {
	p.start()
	p.mu.Lock()
	c := p.conns[name]
	delete(p.conns, name)
	p.mu.Unlock()
	if c != nil {
		c.client.Close()
	}
}

func (p *Pool) reaper() {
	for range time.Tick(30 * time.Second) {
		p.mu.Lock()
		for name, c := range p.conns {
			if c.inUse == 0 && time.Since(c.lastUsed) > p.idle() {
				c.client.Close()
				delete(p.conns, name)
			}
		}
		p.mu.Unlock()
	}
}
