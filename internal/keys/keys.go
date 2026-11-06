// Package keys bootstraps the server's long lived secrets.
//
// On first start the server creates a Secret in its own namespace holding:
//   - session signing key
//   - SSH gateway host key (what users see when connecting to the gateway)
//   - SSH client key (used by the server to log into environments)
//
// Keeping them in a Secret makes them stable across restarts and replicas
// without requiring the installer (Helm, kubectl, ...) to generate them.
package keys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update

const (
	keySession   = "session-key"
	keyHost      = "gateway-host-key"
	keyClient    = "client-key"
	sessionBytes = 32
)

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

// Ensure loads or creates the key Secret. c must be an uncached client.
func Ensure(ctx context.Context, c client.Client, namespace, name string) (*Keys, error) {
	for range 3 {
		sec := &corev1.Secret{}
		err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, sec)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return nil, fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
		}
		if create {
			sec = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "devenv"},
			}}
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		changed := false
		if len(sec.Data[keySession]) == 0 {
			b := make([]byte, sessionBytes)
			if _, err := rand.Read(b); err != nil {
				return nil, err
			}
			sec.Data[keySession] = b
			changed = true
		}
		for _, k := range []string{keyHost, keyClient} {
			if len(sec.Data[k]) == 0 {
				pemBytes, err := newEd25519PEM()
				if err != nil {
					return nil, err
				}
				sec.Data[k] = pemBytes
				changed = true
			}
		}
		if changed {
			if create {
				err = c.Create(ctx, sec)
			} else {
				err = c.Update(ctx, sec)
			}
			if apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) {
				continue // another replica won; reload
			}
			if err != nil {
				return nil, fmt.Errorf("save secret %s/%s: %w", namespace, name, err)
			}
		}
		return parse(sec)
	}
	return nil, fmt.Errorf("could not bootstrap secret %s/%s", namespace, name)
}

func parse(sec *corev1.Secret) (*Keys, error) {
	host, err := ssh.ParsePrivateKey(sec.Data[keyHost])
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", keyHost, err)
	}
	cl, err := ssh.ParsePrivateKey(sec.Data[keyClient])
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", keyClient, err)
	}
	return &Keys{SessionKey: sec.Data[keySession], HostKey: host, ClientKey: cl}, nil
}

func newEd25519PEM() ([]byte, error) {
	_, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(sk, "devenv")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}
