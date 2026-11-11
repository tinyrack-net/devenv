// Package keys bootstraps the server's long lived secrets in the database:
//   - session signing key
//   - SSH gateway host key (what users see when connecting to the gateway)
//   - SSH client key (used by the server to log into environments)
//
// They are generated on first start; every value is written only if absent,
// so concurrently starting replicas agree.
package keys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Store keys (kv table).
const (
	KeySession = "server/session-key"
	KeyHost    = "server/gateway-host-key"
	KeyClient  = "server/client-key"
)

// KV is the key/value part of the store.
type KV interface {
	SetValueIfAbsent(ctx context.Context, key, value string) (string, error)
}

// Keys are the server's secrets.
type Keys struct {
	SessionKey []byte
	HostKey    ssh.Signer
	ClientKey  ssh.Signer
}

// ClientAuthorizedKey returns the authorized_keys line for the client key.
func (k *Keys) ClientAuthorizedKey() string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.ClientKey.PublicKey())))
	return line + " devenv-server"
}

// Ensure loads or creates the keys.
func Ensure(ctx context.Context, kv KV) (*Keys, error) {
	get := func(key string, gen func() (string, error)) (string, error) {
		v, err := gen()
		if err != nil {
			return "", err
		}
		v, err = kv.SetValueIfAbsent(ctx, key, v)
		if err != nil {
			return "", fmt.Errorf("store %s: %w", key, err)
		}
		return v, nil
	}
	session, err := get(KeySession, func() (string, error) {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		return base64.StdEncoding.EncodeToString(b), err
	})
	if err != nil {
		return nil, err
	}
	host, err := get(KeyHost, NewEd25519PEM)
	if err != nil {
		return nil, err
	}
	client, err := get(KeyClient, NewEd25519PEM)
	if err != nil {
		return nil, err
	}
	k := &Keys{}
	if k.SessionKey, err = base64.StdEncoding.DecodeString(session); err != nil {
		return nil, fmt.Errorf("parse session key: %w", err)
	}
	if k.HostKey, err = ssh.ParsePrivateKey([]byte(host)); err != nil {
		return nil, fmt.Errorf("parse gateway host key: %w", err)
	}
	if k.ClientKey, err = ssh.ParsePrivateKey([]byte(client)); err != nil {
		return nil, fmt.Errorf("parse client key: %w", err)
	}
	return k, nil
}

// NewEd25519PEM generates an OpenSSH ed25519 private key.
func NewEd25519PEM() (string, error) {
	_, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(sk, "devenv")
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(block)), nil
}
