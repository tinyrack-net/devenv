// Package sshclient opens SSH connections from the devenv server to
// environments. The SSH gateway, web terminal and VNC tunnel all use it, so
// environments only need to expose sshd: VNC listens on localhost inside the
// environment and is reached through an SSH direct-tcpip channel.
package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
)

// User is the login user inside environment images.
const User = "dev"

// ErrNotRunning is returned when the environment can not accept connections.
var ErrNotRunning = errors.New("environment is not running")

// Dialer connects to environments.
type Dialer struct {
	Client    client.Reader
	Namespace string
	Signer    ssh.Signer
	// Timeout for TCP connect and SSH handshake.
	Timeout time.Duration
	// DialContext overrides the TCP dialer (tests, port-forwarding).
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Environment loads an environment by name.
func (d *Dialer) Environment(ctx context.Context, name string) (*devenvv1.DevEnvironment, error) {
	env := &devenvv1.DevEnvironment{}
	if err := d.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: name}, env); err != nil {
		return nil, err
	}
	return env, nil
}

// Dial opens an SSH client connection to the environment as the dev user.
// The environment's host key is pinned from its Secret.
func (d *Dialer) Dial(ctx context.Context, name string) (*ssh.Client, error) {
	c, chans, reqs, err := d.DialRaw(ctx, name)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// DialRaw is like Dial but returns the low level connection so callers can
// handle channels opened by the environment (remote port forwarding).
func (d *Dialer) DialRaw(ctx context.Context, name string) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	env, err := d.Environment(ctx, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if !env.IsRunning() || env.Status.Phase != devenvv1.PhaseRunning || env.Status.ServiceName == "" {
		return nil, nil, nil, fmt.Errorf("%w (phase %s)", ErrNotRunning, env.Status.Phase)
	}
	sec := &corev1.Secret{}
	if err := d.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: env.Name + "-ssh"}, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, nil, fmt.Errorf("%w: ssh secret missing", ErrNotRunning)
		}
		return nil, nil, nil, err
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(sec.Data[controller.HostPublicKeyFile])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse environment host key: %w", err)
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	addr := net.JoinHostPort(fmt.Sprintf("%s.%s.svc", env.Status.ServiceName, d.Namespace), fmt.Sprint(controller.SSHPort))
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: timeout}).DialContext
	}
	// Right after an environment becomes ready, the Service may not route
	// to the new pod yet (no route to host, connection refused). Retry such
	// errors briefly instead of failing the first terminal or tool call.
	var conn net.Conn
	retryUntil := time.Now().Add(15 * time.Second)
	for {
		conn, err = dial(ctx, "tcp", addr)
		if err == nil || !transient(err) || time.Now().After(retryUntil) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("connect to environment: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	cfg := &ssh.ClientConfig{
		User: User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(d.Signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if !bytes.Equal(key.Marshal(), hostKey.Marshal()) {
				return errors.New("environment host key mismatch")
			}
			return nil
		},
		HostKeyAlgorithms: []string{hostKey.Type()},
		Timeout:           timeout,
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, fmt.Errorf("ssh handshake with environment: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return c, chans, reqs, nil
}

// PasswordHash returns the bcrypt hash of the environment's password, or ""
// when password login is disabled.
func (d *Dialer) PasswordHash(ctx context.Context, name string) (hash, envName string, err error) {
	env, err := d.Environment(ctx, name)
	if err != nil {
		return "", "", err
	}
	sec := &corev1.Secret{}
	if err := d.Client.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: env.Name + "-ssh"}, sec); err != nil {
		return "", "", err
	}
	return string(sec.Data[controller.PasswordHashFile]), env.Name, nil
}

// PushPassword applies a bcrypt hash ("" locks the password) inside a running
// environment immediately, instead of waiting for the kubelet to refresh the
// Secret volume. Images without the helper are left alone.
func (d *Dialer) PushPassword(ctx context.Context, name, hash string) error {
	c, err := d.Dial(ctx, name)
	if err != nil {
		return err
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdin = strings.NewReader(hash + "\n")
	out, err := s.CombinedOutput("if [ -x /usr/local/bin/devenv-password-sync ]; then sudo -n /usr/local/bin/devenv-password-sync --stdin; fi")
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// transient reports connection errors that resolve on their own while a new
// pod is being wired into the Service.
func transient(err error) bool {
	return errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.ECONNRESET)
}
