package api

import (
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// ImageInput creates or replaces a DevImage.
type ImageInput struct {
	Name             string         `json:"name,omitempty"`
	Image            string         `json:"image"`
	DisplayName      string         `json:"displayName,omitempty"`
	Description      string         `json:"description,omitempty"`
	Desktop          bool           `json:"desktop"`
	Disabled         bool           `json:"disabled"`
	ImagePullPolicy  string         `json:"imagePullPolicy,omitempty"`
	DefaultResources ResourcesInput `json:"defaultResources"`
}

func (in *ImageInput) toSpec() (devenvv1.DevImageSpec, error) {
	if in.Image == "" {
		return devenvv1.DevImageSpec{}, badRequest("image is required")
	}
	switch corev1.PullPolicy(in.ImagePullPolicy) {
	case "", corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		return devenvv1.DevImageSpec{}, badRequest("invalid imagePullPolicy")
	}
	res, err := fromResourcesInput(in.DefaultResources)
	if err != nil {
		return devenvv1.DevImageSpec{}, err
	}
	return devenvv1.DevImageSpec{
		Image:            in.Image,
		ImagePullPolicy:  corev1.PullPolicy(in.ImagePullPolicy),
		DisplayName:      in.DisplayName,
		Description:      in.Description,
		Desktop:          in.Desktop,
		Disabled:         in.Disabled,
		DefaultResources: res,
	}, nil
}

func (s *Server) imageUsage(r *http.Request) (map[string]int, error) {
	envs := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(r.Context(), envs, client.InNamespace(s.Namespace)); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, e := range envs.Items {
		out[e.Spec.ImageRef]++
	}
	return out, nil
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListImages(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	img := &devenvv1.DevImage{}
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: r.PathValue("name")}, img); err != nil {
		fail(w, r, err)
		return
	}
	usage, err := s.imageUsage(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toImage(img, usage[img.Name]))
}

func (s *Server) createImage(w http.ResponseWriter, r *http.Request) {
	var in ImageInput
	if !decode(w, r, &in) {
		return
	}
	if errs := validation.IsDNS1123Subdomain(in.Name); len(errs) > 0 {
		fail(w, r, badRequest(fmt.Sprintf("invalid name: %s", errs[0])))
		return
	}
	spec, err := in.toSpec()
	if err != nil {
		fail(w, r, err)
		return
	}
	img := &devenvv1.DevImage{ObjectMeta: metav1.ObjectMeta{Name: in.Name}, Spec: spec}
	if err := s.Client.Create(r.Context(), img); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toImage(img, 0))
}

func (s *Server) updateImage(w http.ResponseWriter, r *http.Request) {
	var in ImageInput
	if !decode(w, r, &in) {
		return
	}
	spec, err := in.toSpec()
	if err != nil {
		fail(w, r, err)
		return
	}
	img := &devenvv1.DevImage{}
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: r.PathValue("name")}, img); err != nil {
		fail(w, r, err)
		return
	}
	img.Spec = spec
	if err := s.Client.Update(r.Context(), img); err != nil {
		fail(w, r, err)
		return
	}
	usage, _ := s.imageUsage(r)
	writeJSON(w, http.StatusOK, toImage(img, usage[img.Name]))
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	usage, err := s.imageUsage(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if n := usage[name]; n > 0 {
		fail(w, r, &httpError{http.StatusConflict, fmt.Sprintf("image is used by %d environment(s); disable it instead", n)})
		return
	}
	img := &devenvv1.DevImage{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := s.Client.Delete(r.Context(), img); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listClasses(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListClasses(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) classCapacity(w http.ResponseWriter, r *http.Request) {
	// ?exclude=<env> gives back the resources of an existing environment's pod,
	// so the UI can show the max when resizing.
	out, err := s.ClassCapacity(r.Context(), r.PathValue("name"), r.URL.Query().Get("exclude"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
