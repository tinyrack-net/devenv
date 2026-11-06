package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/sshclient"
)

func passwordSecretName(env string) string { return env + "-password" }

func validatePassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < controller.MinPasswordLength {
		return badRequest(fmt.Sprintf("password must be at least %d characters", controller.MinPasswordLength))
	}
	if len(pw) > 72 { // bcrypt limit
		return badRequest("password must be at most 72 bytes")
	}
	if strings.ContainsAny(pw, "\x00\n\r") {
		return badRequest("password contains invalid characters")
	}
	return nil
}

// pushPassword applies the change inside the running environment right away
// (best effort; the environment also picks it up from its Secret).
func (s *Server) pushPassword(env *devenvv1.DevEnvironment, hash string) {
	if s.Dialer == nil || !env.IsRunning() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.Dialer.PushPassword(ctx, env.Name, hash); err != nil && !errors.Is(err, sshclient.ErrNotRunning) {
			logf.Log.WithName("api").Info("push password to environment failed; it will apply from the Secret", "environment", env.Name, "error", err.Error())
		}
	}()
}

// storePassword writes the bcrypt hash into the environment's password
// Secret, owned by the environment. The plain password is never stored.
func (s *Server) storePassword(ctx context.Context, env *devenvv1.DevEnvironment, pw string) (string, error) {
	hash, err := controller.HashPassword(pw)
	if err != nil {
		return "", err
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: passwordSecretName(env.Name), Namespace: env.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, s.Client, sec, func() error {
		sec.Labels = map[string]string{
			"app.kubernetes.io/managed-by":    "devenv",
			"devenv.tinyrack.net/environment": env.Name,
			"devenv.tinyrack.net/component":   "password",
		}
		sec.Type = corev1.SecretTypeOpaque
		sec.Data = map[string][]byte{controller.PasswordHashKey: []byte(hash)}
		if env.UID != "" {
			return controllerutil.SetOwnerReference(env, sec, s.Client.Scheme())
		}
		return nil
	})
	return hash, err
}

func (s *Server) deletePassword(ctx context.Context, env string) error {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: passwordSecretName(env), Namespace: s.Namespace}}
	if err := s.Client.Delete(ctx, sec); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// adoptPassword sets the owner reference once the environment exists.
func (s *Server) adoptPassword(ctx context.Context, env *devenvv1.DevEnvironment) error {
	sec := &corev1.Secret{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: env.Namespace, Name: passwordSecretName(env.Name)}, sec); err != nil {
		return err
	}
	patch := client.MergeFrom(sec.DeepCopy())
	if err := controllerutil.SetOwnerReference(env, sec, s.Client.Scheme()); err != nil {
		return err
	}
	return s.Client.Patch(ctx, sec, patch)
}
