package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/auth"
)

// maxNameLength leaves room for child object suffixes such as "-home".
const maxNameLength = 40

func validateName(name string) error {
	if len(name) > maxNameLength {
		return badRequest(fmt.Sprintf("name must be at most %d characters", maxNameLength))
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return badRequest("invalid name: " + errs[0])
	}
	return nil
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	list := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(r.Context(), list, client.InNamespace(s.Namespace)); err != nil {
		fail(w, r, err)
		return
	}
	out := make([]Environment, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toEnvironment(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getEnv(ctx context.Context, name string) (*devenvv1.DevEnvironment, error) {
	env := &devenvv1.DevEnvironment{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, env); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, err := s.getEnv(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEnvironment(env))
}

// admit validates an environment spec against its image, class and cluster capacity.
// exclude is the current pod of the environment when updating.
func (s *Server) admit(ctx context.Context, env *devenvv1.DevEnvironment, newImage bool, checkCapacity bool) error {
	img := &devenvv1.DevImage{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: env.Spec.ImageRef}, img); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return badRequest(fmt.Sprintf("image %q not found", env.Spec.ImageRef))
		}
		return err
	}
	if newImage && img.Spec.Disabled {
		return badRequest(fmt.Sprintf("image %q is disabled", env.Spec.ImageRef))
	}
	class := &devenvv1.DevEnvironmentClass{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: env.ClassName()}, class); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return badRequest(fmt.Sprintf("class %q not found", env.ClassName()))
		}
		return err
	}
	res := devenvv1.Resolve(env.Spec.Resources, &img.Spec, &class.Spec)
	if err := class.Spec.CheckLimits(res); err != nil {
		return badRequest(err.Error())
	}
	if !checkCapacity || !env.IsRunning() {
		return nil
	}
	var exclude *client.ObjectKey
	if env.Name != "" {
		exclude = &client.ObjectKey{Namespace: s.Namespace, Name: env.Name}
	}
	rep, err := s.Capacity.Report(ctx, &class.Spec, exclude)
	if err != nil {
		return err
	}
	if err := rep.Fits(res.CPU, res.Memory); err != nil {
		return &httpError{http.StatusUnprocessableEntity, err.Error()}
	}
	return nil
}

// CreateOptions are extra settings applied by callers other than the console.
type CreateOptions struct {
	// Labels added to the environment.
	Labels map[string]string
}

// CreateEnvironment validates and creates an environment owned by user.
func (s *Server) CreateEnvironment(ctx context.Context, user string, in CreateEnvironmentRequest, opts CreateOptions) (*devenvv1.DevEnvironment, error) {
	if err := validateName(in.Name); err != nil {
		return nil, err
	}
	if in.Image == "" {
		return nil, badRequest("image is required")
	}
	res, err := fromResourcesInput(in.Resources)
	if err != nil {
		return nil, err
	}
	ports, err := fromPortInputs(in.Ports)
	if err != nil {
		return nil, err
	}
	if in.Password != "" {
		if err := validatePassword(in.Password); err != nil {
			return nil, err
		}
	}

	var keys []string
	if in.SSHKeys != nil {
		if keys, err = normalizeKeys(*in.SSHKeys); err != nil {
			return nil, err
		}
	} else {
		stored, err := s.loadKeys(ctx, user)
		if err != nil {
			return nil, err
		}
		for _, k := range stored {
			keys = append(keys, k.PublicKey)
		}
	}

	env := &devenvv1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: in.Name, Namespace: s.Namespace, Labels: opts.Labels},
		Spec: devenvv1.DevEnvironmentSpec{
			Owner:             user,
			ImageRef:          in.Image,
			ClassName:         in.Class,
			Resources:         res,
			Running:           ptr.To(in.Running == nil || *in.Running),
			SSHAuthorizedKeys: keys,
			Ports:             ports,
		},
	}
	// Name is set but there is no pod yet, so excluding it is harmless.
	if err := s.admit(ctx, env, true, true); err != nil {
		return nil, err
	}
	if in.Password != "" {
		// Exists check first so a stale password Secret is never reused.
		if err := s.Client.Get(ctx, client.ObjectKeyFromObject(env), &devenvv1.DevEnvironment{}); err == nil {
			return nil, &httpError{http.StatusConflict, "already exists"}
		}
		if _, err := s.storePassword(ctx, env, in.Password); err != nil {
			return nil, err
		}
		env.Spec.PasswordSecretName = passwordSecretName(env.Name)
	}
	if err := s.Client.Create(ctx, env); err != nil {
		if in.Password != "" {
			_ = s.deletePassword(ctx, env.Name)
		}
		return nil, err
	}
	if in.Password != "" {
		if err := s.adoptPassword(ctx, env); err != nil {
			logf.FromContext(ctx).Error(err, "adopt password secret", "environment", env.Name)
		}
	}
	return env, nil
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var in CreateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	env, err := s.CreateEnvironment(r.Context(), auth.UserFrom(r.Context()).Name, in, CreateOptions{})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toEnvironment(env))
}

// UpdateEnvironment applies a partial update.
func (s *Server) UpdateEnvironment(ctx context.Context, name string, in UpdateEnvironmentRequest) (*devenvv1.DevEnvironment, error) {
	env, err := s.getEnv(ctx, name)
	if err != nil {
		return nil, err
	}
	before := env.DeepCopy()
	if in.Image != nil {
		env.Spec.ImageRef = *in.Image
	}
	if in.Resources != nil {
		res, err := fromResourcesInput(*in.Resources)
		if err != nil {
			return nil, err
		}
		env.Spec.Resources = res
	}
	if in.SSHKeys != nil {
		keys, err := normalizeKeys(*in.SSHKeys)
		if err != nil {
			return nil, err
		}
		env.Spec.SSHAuthorizedKeys = keys
	}
	if in.Running != nil {
		env.Spec.Running = ptr.To(*in.Running)
	}
	if in.Ports != nil {
		ports, err := fromPortInputs(*in.Ports)
		if err != nil {
			return nil, err
		}
		env.Spec.Ports = ports
	}

	removePassword := false
	pushHash := ""
	if in.Password != nil {
		if *in.Password == "" {
			env.Spec.PasswordSecretName = ""
			removePassword = true
		} else {
			if err := validatePassword(*in.Password); err != nil {
				return nil, err
			}
			hash, err := s.storePassword(ctx, env, *in.Password)
			if err != nil {
				return nil, err
			}
			pushHash = hash
			env.Spec.PasswordSecretName = passwordSecretName(env.Name)
		}
	}

	imageChanged := env.Spec.ImageRef != before.Spec.ImageRef
	needsCapacity := imageChanged || in.Resources != nil || (env.IsRunning() && !before.IsRunning())
	if err := s.admit(ctx, env, imageChanged, needsCapacity); err != nil {
		return nil, err
	}
	if err := s.Client.Patch(ctx, env, client.MergeFrom(before)); err != nil {
		return nil, err
	}
	if removePassword {
		if err := s.deletePassword(ctx, env.Name); err != nil {
			logf.FromContext(ctx).Error(err, "delete password secret", "environment", env.Name)
		}
	}
	if in.Password != nil {
		s.pushPassword(env, pushHash)
	}
	return env, nil
}

func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	var in UpdateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	env, err := s.UpdateEnvironment(r.Context(), r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEnvironment(env))
}

// SetRunning starts or stops an environment.
func (s *Server) SetRunning(ctx context.Context, name string, running bool) (*devenvv1.DevEnvironment, error) {
	env, err := s.getEnv(ctx, name)
	if err != nil {
		return nil, err
	}
	if env.IsRunning() == running {
		return env, nil
	}
	before := env.DeepCopy()
	env.Spec.Running = ptr.To(running)
	if running {
		if err := s.admit(ctx, env, false, true); err != nil {
			return nil, err
		}
	}
	if err := s.Client.Patch(ctx, env, client.MergeFrom(before)); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Server) setRunning(w http.ResponseWriter, r *http.Request, running bool) {
	env, err := s.SetRunning(r.Context(), r.PathValue("name"), running)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEnvironment(env))
}

func (s *Server) startEnvironment(w http.ResponseWriter, r *http.Request) { s.setRunning(w, r, true) }
func (s *Server) stopEnvironment(w http.ResponseWriter, r *http.Request)  { s.setRunning(w, r, false) }

// DeleteEnvironment deletes an environment and, through owner references,
// its pod, home volume and secrets.
func (s *Server) DeleteEnvironment(ctx context.Context, name string) error {
	env := &devenvv1.DevEnvironment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}}
	// Foreground deletion keeps the environment visible until children are gone.
	return s.Client.Delete(ctx, env, client.PropagationPolicy(metav1.DeletePropagationForeground))
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteEnvironment(r.Context(), r.PathValue("name")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// GetEnvironment returns an environment.
func (s *Server) GetEnvironment(ctx context.Context, name string) (*devenvv1.DevEnvironment, error) {
	return s.getEnv(ctx, name)
}

// ListEnvironments returns all environments sorted by name.
func (s *Server) ListEnvironments(ctx context.Context, opts ...client.ListOption) ([]devenvv1.DevEnvironment, error) {
	list := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(ctx, list, append([]client.ListOption{client.InNamespace(s.Namespace)}, opts...)...); err != nil {
		return nil, err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	return list.Items, nil
}

// +kubebuilder:rbac:groups="",resources=events,verbs=list

func (s *Server) environmentEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.getEnv(r.Context(), name); err != nil {
		fail(w, r, err)
		return
	}
	list := &corev1.EventList{}
	if err := s.Reader.List(r.Context(), list, client.InNamespace(s.Namespace)); err != nil {
		fail(w, r, err)
		return
	}
	related := map[string]bool{name: true, name + "-home": true, name + "-ssh": true}
	out := []Event{}
	for _, e := range list.Items {
		if !related[e.InvolvedObject.Name] {
			continue
		}
		t := e.LastTimestamp.Time
		if t.IsZero() {
			t = e.EventTime.Time
		}
		if t.IsZero() {
			t = e.CreationTimestamp.Time
		}
		// events.k8s.io "note" is stored as core/v1 "message".
		msg := e.Message
		out = append(out, Event{
			Type:    e.Type,
			Reason:  e.Reason,
			Message: strings.TrimSpace(msg),
			Object:  strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name,
			Count:   e.Count,
			Time:    t,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if len(out) > 100 {
		out = out[:100]
	}
	writeJSON(w, http.StatusOK, out)
}
