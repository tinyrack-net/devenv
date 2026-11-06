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

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var in CreateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	if err := validateName(in.Name); err != nil {
		fail(w, r, err)
		return
	}
	if in.Image == "" {
		fail(w, r, badRequest("image is required"))
		return
	}
	res, err := fromResourcesInput(in.Resources)
	if err != nil {
		fail(w, r, err)
		return
	}
	user := auth.UserFrom(r.Context())

	var keys []string
	if in.SSHKeys != nil {
		if keys, err = normalizeKeys(*in.SSHKeys); err != nil {
			fail(w, r, err)
			return
		}
	} else {
		stored, err := s.loadKeys(r.Context(), user.Name)
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, k := range stored {
			keys = append(keys, k.PublicKey)
		}
	}

	env := &devenvv1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: in.Name, Namespace: s.Namespace},
		Spec: devenvv1.DevEnvironmentSpec{
			Owner:             user.Name,
			ImageRef:          in.Image,
			ClassName:         in.Class,
			Resources:         res,
			Running:           ptr.To(in.Running == nil || *in.Running),
			SSHAuthorizedKeys: keys,
		},
	}
	// Name is set but there is no pod yet, so excluding it is harmless.
	if err := s.admit(r.Context(), env, true, true); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.Client.Create(r.Context(), env); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toEnvironment(env))
}

func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	var in UpdateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	env, err := s.getEnv(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	before := env.DeepCopy()
	if in.Image != nil {
		env.Spec.ImageRef = *in.Image
	}
	if in.Resources != nil {
		res, err := fromResourcesInput(*in.Resources)
		if err != nil {
			fail(w, r, err)
			return
		}
		env.Spec.Resources = res
	}
	if in.SSHKeys != nil {
		keys, err := normalizeKeys(*in.SSHKeys)
		if err != nil {
			fail(w, r, err)
			return
		}
		env.Spec.SSHAuthorizedKeys = keys
	}
	if in.Running != nil {
		env.Spec.Running = ptr.To(*in.Running)
	}

	imageChanged := env.Spec.ImageRef != before.Spec.ImageRef
	needsCapacity := imageChanged || in.Resources != nil || (env.IsRunning() && !before.IsRunning())
	if err := s.admit(r.Context(), env, imageChanged, needsCapacity); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.Client.Patch(r.Context(), env, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEnvironment(env))
}

func (s *Server) setRunning(w http.ResponseWriter, r *http.Request, running bool) {
	env, err := s.getEnv(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if env.IsRunning() == running {
		writeJSON(w, http.StatusOK, toEnvironment(env))
		return
	}
	before := env.DeepCopy()
	env.Spec.Running = ptr.To(running)
	if running {
		if err := s.admit(r.Context(), env, false, true); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := s.Client.Patch(r.Context(), env, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEnvironment(env))
}

func (s *Server) startEnvironment(w http.ResponseWriter, r *http.Request) { s.setRunning(w, r, true) }
func (s *Server) stopEnvironment(w http.ResponseWriter, r *http.Request)  { s.setRunning(w, r, false) }

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	env := &devenvv1.DevEnvironment{ObjectMeta: metav1.ObjectMeta{Name: r.PathValue("name"), Namespace: s.Namespace}}
	// Foreground deletion keeps the environment visible until children are gone.
	if err := s.Client.Delete(r.Context(), env, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
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
