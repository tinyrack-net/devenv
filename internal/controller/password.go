package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

const (
	// Keys of the user supplied password Secret.
	PasswordKey     = "password"
	PasswordHashKey = "passwordHash"

	// PasswordHashFile is the key in the environment SSH Secret holding the
	// bcrypt hash of the dev user's password. Empty disables password login.
	PasswordHashFile = "password_hash"

	// annotationPasswordSource records which plain password the stored hash
	// was computed from (sha256), so it is not re-hashed on every reconcile.
	annotationPasswordSource = "devenv.tinyrack.net/password-source"

	bcryptCost = 10
)

// MinPasswordLength is enforced by the API.
const MinPasswordLength = 8

// HashPassword returns a bcrypt hash suitable for /etc/shadow and the gateway.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	return string(b), err
}

func isBcrypt(h string) bool {
	return strings.HasPrefix(h, "$2a$") || strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$")
}

func sourceDigest(pw string) string {
	sum := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(sum[:])
}

// resolvePassword returns the bcrypt hash for the environment's password, or
// "" when password login is disabled. current is the existing SSH Secret.
func (r *DevEnvironmentReconciler) resolvePassword(ctx context.Context, env *devenvv1.DevEnvironment, current *corev1.Secret) (hash, source string, err error) {
	if env.Spec.PasswordSecretName == "" {
		return "", "", nil
	}
	sec := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: env.Namespace, Name: env.Spec.PasswordSecretName}, sec); err != nil {
		if apierrors.IsNotFound(err) {
			return "", "", fmt.Errorf("password secret %q not found", env.Spec.PasswordSecretName)
		}
		return "", "", err
	}
	// Restored password Secrets reference the UID of the old environment;
	// point them at this one so they are not garbage collected.
	if sec.Labels["devenv.tinyrack.net/component"] == "password" {
		for _, ref := range sec.GetOwnerReferences() {
			if ref.Kind == "DevEnvironment" && ref.Name == env.Name && ref.UID != env.UID {
				before := sec.DeepCopy()
				refs := []metav1.OwnerReference{}
				for _, r2 := range sec.GetOwnerReferences() {
					if !(r2.Kind == "DevEnvironment" && r2.Name == env.Name) {
						refs = append(refs, r2)
					}
				}
				refs = append(refs, metav1.OwnerReference{APIVersion: devenvv1.GroupVersion.String(), Kind: "DevEnvironment", Name: env.Name, UID: env.UID})
				sec.SetOwnerReferences(refs)
				if err := r.Patch(ctx, sec, client.MergeFrom(before)); err != nil {
					return "", "", err
				}
				break
			}
		}
	}
	if h := strings.TrimSpace(string(sec.Data[PasswordHashKey])); h != "" {
		if !isBcrypt(h) {
			return "", "", fmt.Errorf("password secret %q: %s must be a bcrypt hash", sec.Name, PasswordHashKey)
		}
		return h, "", nil
	}
	pw := string(sec.Data[PasswordKey])
	if pw == "" {
		return "", "", fmt.Errorf("password secret %q has neither %s nor %s", sec.Name, PasswordHashKey, PasswordKey)
	}
	digest := sourceDigest(pw)
	if current != nil && current.Annotations[annotationPasswordSource] == digest {
		if h := string(current.Data[PasswordHashFile]); isBcrypt(h) {
			return h, digest, nil
		}
	}
	h, err := HashPassword(pw)
	return h, digest, err
}
