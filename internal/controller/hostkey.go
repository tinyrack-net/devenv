package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"

	"golang.org/x/crypto/ssh"
)

// generateHostKey creates an OpenSSH ed25519 host key pair. Keeping the host
// key in the environment Secret makes it stable across pod restarts, so the
// SSH gateway can pin it and users never see host key warnings.
func generateHostKey() (priv, pub []byte, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(sk, "devenv")
	if err != nil {
		return nil, nil, err
	}
	sshPub, err := ssh.NewPublicKey(pk)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(sshPub), nil
}
