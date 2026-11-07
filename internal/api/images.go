package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/auth"
)

// ImageInput creates or replaces a DevImage.
type ImageInput struct {
	Name             string         `json:"name,omitempty"`
	Image            string         `json:"image"`
	DisplayName      string         `json:"displayName,omitempty"`
	Description      string         `json:"description,omitempty"`
	Desktop          bool           `json:"desktop"`
	Disabled         bool           `json:"disabled"`
	Prepull          bool           `json:"prepull,omitempty"`
	ImagePullPolicy  string         `json:"imagePullPolicy,omitempty"`
	DefaultResources ResourcesInput `json:"defaultResources"`
	// Shared creates a shared image (admins only). Otherwise the image is
	// personal to the caller.
	Shared bool         `json:"shared,omitempty"`
	Access *AccessInput `json:"access,omitempty"`
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
	acc, err := fromAccessInput(in.Access)
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
		Prepull:          in.Prepull,
		DefaultResources: res,
		Access:           acc,
	}, nil
}

func (s *Server) imageUsage(ctx context.Context) (map[string]int, error) {
	envs := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(ctx, envs, client.InNamespace(s.Namespace)); err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, e := range envs.Items {
		out[e.Spec.ImageRef]++
	}
	return out, nil
}

func (s *Server) imageView(p *auth.Principal, img *devenvv1.DevImage, inUse int) Image {
	out := toImage(img, inUse)
	out.CanManage = access.CanManageImage(p, img)
	return out
}

// ListImages returns the images the principal can use, with usage counts.
// Admins also see other users' personal images when all is true.
func (s *Server) ListImages(ctx context.Context, p *auth.Principal, all bool) ([]Image, error) {
	list := &devenvv1.DevImageList{}
	if err := s.Client.List(ctx, list); err != nil {
		return nil, err
	}
	usage, err := s.imageUsage(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(list.Items))
	for i := range list.Items {
		img := &list.Items[i]
		if !access.CanUseImage(p, img) {
			continue
		}
		if p.Admin && !all && img.Spec.Owner != "" && img.Spec.Owner != p.Name {
			continue
		}
		out = append(out, s.imageView(p, img, usage[img.Name]))
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Owner == "") != (out[j].Owner == "") {
			return out[i].Owner == ""
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Server) listImages(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListImages(r.Context(), principal(r), r.URL.Query().Get("all") != "")
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadImage(ctx context.Context, p *auth.Principal, name string, manage bool) (*devenvv1.DevImage, error) {
	img := &devenvv1.DevImage{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name}, img); err != nil {
		return nil, err
	}
	if !access.CanUseImage(p, img) {
		return nil, errNotFound
	}
	if manage && !access.CanManageImage(p, img) {
		return nil, forbidden("only administrators can change shared images")
	}
	return img, nil
}

func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	img, err := s.loadImage(r.Context(), p, r.PathValue("name"), false)
	if err != nil {
		fail(w, r, err)
		return
	}
	usage, err := s.imageUsage(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.imageView(p, img, usage[img.Name]))
}

func (s *Server) createImage(w http.ResponseWriter, r *http.Request) {
	var in ImageInput
	if !decode(w, r, &in) {
		return
	}
	p := principal(r)
	if errs := validation.IsDNS1123Subdomain(in.Name); len(errs) > 0 {
		fail(w, r, badRequest(fmt.Sprintf("invalid name: %s", errs[0])))
		return
	}
	spec, err := in.toSpec()
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Shared {
		if !p.Admin {
			fail(w, r, forbidden("only administrators can create shared images"))
			return
		}
	} else {
		spec.Owner = p.Name
		spec.Access = devenvv1.Access{}
		if !p.Admin {
			// Prepulling to every node is a cluster wide decision.
			spec.Prepull = false
		}
	}
	img := &devenvv1.DevImage{ObjectMeta: metav1.ObjectMeta{Name: in.Name}, Spec: spec}
	if spec.Owner != "" {
		img.Labels = map[string]string{devenvv1.LabelOwner: spec.Owner}
	}
	if err := s.Client.Create(r.Context(), img); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.imageView(p, img, 0))
}

func (s *Server) updateImage(w http.ResponseWriter, r *http.Request) {
	var in ImageInput
	if !decode(w, r, &in) {
		return
	}
	p := principal(r)
	spec, err := in.toSpec()
	if err != nil {
		fail(w, r, err)
		return
	}
	img, err := s.loadImage(r.Context(), p, r.PathValue("name"), true)
	if err != nil {
		fail(w, r, err)
		return
	}
	// Ownership does not change through updates.
	spec.Owner = img.Spec.Owner
	if spec.Owner != "" {
		spec.Access = devenvv1.Access{}
		if !p.Admin {
			spec.Prepull = img.Spec.Prepull
		}
	}
	img.Spec = spec
	if err := s.Client.Update(r.Context(), img); err != nil {
		fail(w, r, err)
		return
	}
	usage, _ := s.imageUsage(r.Context())
	writeJSON(w, http.StatusOK, s.imageView(p, img, usage[img.Name]))
}

func (s *Server) deleteImage(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	img, err := s.loadImage(r.Context(), p, r.PathValue("name"), true)
	if err != nil {
		fail(w, r, err)
		return
	}
	usage, err := s.imageUsage(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	if n := usage[img.Name]; n > 0 {
		fail(w, r, &httpError{http.StatusConflict, fmt.Sprintf("image is used by %d environment(s); disable it instead", n)})
		return
	}
	if err := s.Client.Delete(r.Context(), img, client.Preconditions{UID: &img.UID}); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListClasses returns the classes the principal can use, default first.
func (s *Server) ListClasses(ctx context.Context, p *auth.Principal) ([]Class, error) {
	list := &devenvv1.DevEnvironmentClassList{}
	if err := s.Client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]Class, 0, len(list.Items))
	for i := range list.Items {
		if access.CanUseClass(p, &list.Items[i]) {
			out = append(out, toClass(&list.Items[i]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Server) listClasses(w http.ResponseWriter, r *http.Request) {
	out, err := s.ListClasses(r.Context(), principal(r))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) loadClass(ctx context.Context, p *auth.Principal, name string) (*devenvv1.DevEnvironmentClass, error) {
	c := &devenvv1.DevEnvironmentClass{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name}, c); err != nil {
		return nil, err
	}
	if !access.CanUseClass(p, c) {
		return nil, errNotFound
	}
	return c, nil
}

func (s *Server) getClass(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	c, err := s.loadClass(r.Context(), p, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	out := ClassDetail{Class: toClass(c)}
	if p.Admin {
		out.Spec, _ = json.Marshal(c.Spec)
	}
	writeJSON(w, http.StatusOK, out)
}

// updateClassAccess changes who may use a class (admin). The rest of the class
// spec is managed declaratively (Helm/GitOps).
func (s *Server) updateClassAccess(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in AccessInput
	if !decode(w, r, &in) {
		return
	}
	acc, err := fromAccessInput(&in)
	if err != nil {
		fail(w, r, err)
		return
	}
	c := &devenvv1.DevEnvironmentClass{}
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: r.PathValue("name")}, c); err != nil {
		fail(w, r, err)
		return
	}
	before := c.DeepCopy()
	c.Spec.Access = acc
	if err := s.Client.Patch(r.Context(), c, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toClass(c))
}

// ClassCapacity returns what can currently be requested in a class.
func (s *Server) ClassCapacity(ctx context.Context, p *auth.Principal, className, exclude string) (*Capacity, error) {
	class, err := s.loadClass(ctx, p, className)
	if err != nil {
		return nil, err
	}
	var ex *client.ObjectKey
	if exclude != "" {
		if _, _, err := s.Authorize(ctx, p, exclude, 1); err == nil {
			ex = &client.ObjectKey{Namespace: s.Namespace, Name: exclude}
		}
	}
	rep, err := s.Capacity.Report(ctx, &class.Spec, ex)
	if err != nil {
		return nil, err
	}
	out := &Capacity{Class: class.Name, MaxCPU: rep.Max.CPU.String(), MaxMem: rep.Max.Memory.String(), Nodes: rep.Nodes}
	if rep.MaxDisk != nil {
		out.MaxDisk = rep.MaxDisk.String()
	}
	if out.Nodes == nil {
		out.Nodes = []capacityNode{}
	}
	return out, nil
}

func (s *Server) classCapacity(w http.ResponseWriter, r *http.Request) {
	// ?exclude=<env> gives back the resources of an existing environment's pod,
	// so the UI can show the max when resizing.
	out, err := s.ClassCapacity(r.Context(), principal(r), r.PathValue("name"), r.URL.Query().Get("exclude"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
