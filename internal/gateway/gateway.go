// Package gateway implements the SSH gateway.
//
// Users connect with `ssh -p 2222 <environment>@<host>`. The gateway
// authenticates the public key against the environment's authorized keys,
// then opens its own SSH connection to the environment and relays channels
// and requests in both directions. This supports shells, exec (scp, rsync,
// VS Code / JetBrains remote), SFTP, agent forwarding, local (-L) and
// remote (-R) port forwarding.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/tinyrack-net/devenv/internal/sshclient"
)

const permEnv = "devenv-environment"

// Gateway is an SSH server proxying to environments.
type Gateway struct {
	Addr    string
	HostKey ssh.Signer
	Dialer  *sshclient.Dialer
	// Banner is shown before authentication. Optional.
	Banner string

	log logr.Logger
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (g *Gateway) NeedLeaderElection() bool { return false }

// Start implements manager.Runnable.
func (g *Gateway) Start(ctx context.Context) error {
	g.log = logf.FromContext(ctx).WithName("ssh-gateway")
	ln, err := net.Listen("tcp", g.Addr)
	if err != nil {
		return err
	}
	g.log.Info("listening", "addr", g.Addr)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go g.handle(ctx, conn)
	}
}

func (g *Gateway) config(ctx context.Context) *ssh.ServerConfig {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return g.authorize(ctx, meta.User(), key)
		},
		PasswordCallback: func(meta ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			return g.authorizePassword(ctx, meta.User(), pw)
		},
		MaxAuthTries:   6,
		BannerCallback: func(ssh.ConnMetadata) string { return g.Banner },
		ServerVersion:  "SSH-2.0-devenv",
	}
	cfg.AddHostKey(g.HostKey)
	return cfg
}

// authorize checks the key against the environment's authorized keys.
func (g *Gateway) authorize(ctx context.Context, user string, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	env, err := g.Dialer.Environment(ctx, user)
	if err != nil {
		return nil, errors.New("unknown environment")
	}
	want := key.Marshal()
	for _, line := range env.Spec.SSHAuthorizedKeys {
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err == nil && bytes.Equal(k.Marshal(), want) {
			return &ssh.Permissions{Extensions: map[string]string{permEnv: env.Name}}, nil
		}
	}
	return nil, errors.New("key not authorized")
}

// authorizePassword checks the password against the environment's bcrypt
// hash (the same hash the environment uses in /etc/shadow).
func (g *Gateway) authorizePassword(ctx context.Context, user string, pw []byte) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	hash, envName, err := g.Dialer.PasswordHash(ctx, user)
	if err != nil || hash == "" {
		// Spend comparable time so missing environments are not distinguishable.
		_ = bcrypt.CompareHashAndPassword(dummyHash, pw)
		time.Sleep(time.Second)
		return nil, errors.New("password login not available")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), pw) != nil {
		time.Sleep(time.Second)
		return nil, errors.New("wrong password")
	}
	return &ssh.Permissions{Extensions: map[string]string{permEnv: envName}}, nil
}

// dummyHash is compared against when no hash exists, to equalize timing.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("devenv-dummy-password"), 10)

func (g *Gateway) handle(ctx context.Context, nConn net.Conn) {
	defer nConn.Close()
	_ = nConn.SetDeadline(time.Now().Add(30 * time.Second))
	sConn, chans, reqs, err := ssh.NewServerConn(nConn, g.config(ctx))
	if err != nil {
		g.log.V(1).Info("handshake failed", "remote", nConn.RemoteAddr().String(), "error", err.Error())
		return
	}
	_ = nConn.SetDeadline(time.Time{})
	defer sConn.Close()
	envName := sConn.Permissions.Extensions[permEnv]
	log := g.log.WithValues("environment", envName, "remote", sConn.RemoteAddr().String())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var up ssh.Conn
	var upChans <-chan ssh.NewChannel
	var upReqs <-chan *ssh.Request
	for attempt := 0; ; attempt++ {
		up, upChans, upReqs, err = g.Dialer.DialRaw(ctx, envName)
		if err == nil || errors.Is(err, sshclient.ErrNotRunning) || attempt >= 5 || ctx.Err() != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		msg := "environment unavailable"
		if errors.Is(err, sshclient.ErrNotRunning) {
			msg = "environment is not running; start it from the web console"
		}
		log.Info("upstream dial failed", "error", err.Error())
		rejectAll(chans, msg)
		return
	}
	defer up.Close()
	log.Info("session started")

	var wg sync.WaitGroup
	wg.Add(4)
	// Global requests (tcpip-forward, keepalive...) in both directions.
	go func() { defer wg.Done(); forwardRequests(reqs, up); cancel() }()
	go func() { defer wg.Done(); forwardRequests(upReqs, sConn); cancel() }()
	// Channels opened by the client (session, direct-tcpip).
	go func() { defer wg.Done(); proxyChannels(ctx, chans, up); cancel() }()
	// Channels opened by the environment (forwarded-tcpip, auth-agent, x11).
	go func() { defer wg.Done(); proxyChannels(ctx, upChans, sConn); cancel() }()

	go func() {
		<-ctx.Done()
		_ = sConn.Close()
		_ = up.Close()
	}()
	wg.Wait()
	log.Info("session ended")
}

func rejectAll(chans <-chan ssh.NewChannel, msg string) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ch, ok := <-chans:
			if !ok {
				return
			}
			if ch.ChannelType() == "session" {
				// Print the reason to the user's terminal.
				c, reqs, err := ch.Accept()
				if err != nil {
					continue
				}
				// Wait for the shell/exec request so the client is reading.
				for req := range reqs {
					ok := req.Type == "shell" || req.Type == "exec" || req.Type == "subsystem"
					if req.WantReply {
						_ = req.Reply(ok || req.Type == "pty-req" || req.Type == "env", nil)
					}
					if ok {
						break
					}
				}
				go ssh.DiscardRequests(reqs)
				fmt.Fprintf(c.Stderr(), "devenv: %s\r\n", msg)
				_, _ = c.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{1}))
				_ = c.Close()
				return
			}
			_ = ch.Reject(ssh.ConnectionFailed, msg)
		case <-timer.C:
			return
		}
	}
}

// opener is implemented by both ssh.Conn sides.
type opener interface {
	OpenChannel(name string, data []byte) (ssh.Channel, <-chan *ssh.Request, error)
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

func forwardRequests(in <-chan *ssh.Request, out opener) {
	for req := range in {
		ok, payload, err := out.SendRequest(req.Type, req.WantReply, req.Payload)
		if req.WantReply {
			if err != nil {
				ok = false
			}
			_ = req.Reply(ok, payload)
		}
	}
}

func proxyChannels(ctx context.Context, in <-chan ssh.NewChannel, out opener) {
	for {
		select {
		case <-ctx.Done():
			return
		case nc, ok := <-in:
			if !ok {
				return
			}
			go proxyChannel(nc, out)
		}
	}
}

func proxyChannel(nc ssh.NewChannel, out opener) {
	dst, dstReqs, err := out.OpenChannel(nc.ChannelType(), nc.ExtraData())
	if err != nil {
		var oe *ssh.OpenChannelError
		if errors.As(err, &oe) {
			_ = nc.Reject(oe.Reason, oe.Message)
		} else {
			_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		}
		return
	}
	src, srcReqs, err := nc.Accept()
	if err != nil {
		_ = dst.Close()
		return
	}
	pipe(src, srcReqs, dst, dstReqs)
}

// pipe relays data and channel requests between the client channel a and
// the environment channel b. The environment side drives the lifecycle: when
// b closes (process exited, forwarded connection ended), remaining output is
// drained to a and both are closed. When the client closes a, b is closed.
func pipe(a ssh.Channel, aReqs <-chan *ssh.Request, b ssh.Channel, bReqs <-chan *ssh.Request) {
	// Client -> environment.
	go func() {
		_, _ = io.Copy(b, a)
		_ = b.CloseWrite()
	}()
	go func() { _, _ = io.Copy(b.Stderr(), a.Stderr()) }()
	// inflight is held while a client request is being relayed, so the
	// channel is not closed before the client receives its reply (a fast
	// command can exit before its exec request reply is relayed).
	var inflight sync.Mutex
	go func() {
		for req := range aReqs {
			inflight.Lock()
			ok, err := b.SendRequest(req.Type, req.WantReply, req.Payload)
			if req.WantReply {
				_ = req.Reply(ok && err == nil, nil)
			}
			inflight.Unlock()
		}
		// Client closed the channel.
		_ = b.Close()
	}()

	// Environment -> client.
	var out sync.WaitGroup
	out.Add(2)
	go func() { defer out.Done(); _, _ = io.Copy(a, b) }()
	go func() { defer out.Done(); _, _ = io.Copy(a.Stderr(), b.Stderr()) }()

	// exit-status/exit-signal are held back until all output has been
	// written, otherwise clients may exit before receiving the tail.
	var final []*ssh.Request
	for req := range bReqs {
		if req.Type == "exit-status" || req.Type == "exit-signal" {
			final = append(final, req)
			continue
		}
		ok, err := a.SendRequest(req.Type, req.WantReply, req.Payload)
		if req.WantReply {
			_ = req.Reply(ok && err == nil, nil)
		}
	}
	out.Wait()
	inflight.Lock()
	defer inflight.Unlock()
	for _, req := range final {
		_, _ = a.SendRequest(req.Type, false, req.Payload)
	}
	_ = a.CloseWrite()
	_ = a.Close()
	_ = b.Close()
}
