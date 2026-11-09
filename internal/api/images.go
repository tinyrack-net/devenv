package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/model"
)

// ImageInput creates or replaces an image.
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

func (in *ImageInput) toSpec() (model.ImageSpec, error) {
	if in.Image == "" {
		return model.ImageSpec{}, badRequest("image is required")
	}
	switch corev1.PullPolicy(in.ImagePullPolicy) {
	case "", corev1.PullAlways, corev1.PullIfNotPresent, corev1.PullNever:
	default:
		return model.ImageSpec{}, badRequest("invalid imagePullPolicy")
	}
	res, err := fromResourcesInput(in.DefaultResources)
	if err != nil {
		return model.ImageSpec{}, err
	}
	acc, err := fromAccessInput(in.Access)
	if err != nil {
		return model.ImageSpec{}, err
	}
	return model.ImageSpec{
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
	envs, err := s.DB.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, e := range envs {
		out[e.Spec.ImageRef]++
	}
	return out, nil
}

func (s *Server) imageView(p *auth.Principal, img *model.Image, inUse int) Image {
	out := toImage(img, inUse)
	out.CanManage = access.CanManageImage(p, img)
	return out
}

// ListImages returns the images the principal can use, with usage counts.
// Admins also see other users' personal images when all is true.
func (s *Server) ListImages(ctx context.Context, p *auth.Principal, all bool) ([]Image, error) {
	list, err := s.Catalog.Images(ctx)
	if err != nil {
		return nil, err
	}
	usage, err := s.imageUsage(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(list))
	for i := range list {
		img := &list[i]
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

func (s *Server) loadImage(ctx context.Context, p *auth.Principal, name string, manage bool) (*model.Image, error) {
	img, err := s.Catalog.Image(ctx, name)
	if err != nil {
		return nil, err
	}
	if !access.CanUseImage(p, img) {
		return nil, errNotFound
	}
	if manage && img.Managed {
		return nil, forbidden("this image is part of the deployment configuration; change it there")
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
		spec.Access = model.Access{}
		if !p.Admin {
			// Prepulling to every node is a cluster wide decision.
			spec.Prepull = false
		}
	}
	if s.Catalog.Managed(in.Name) {
		fail(w, r, &httpError{http.StatusConflict, "already exists"})
		return
	}
	img := &model.Image{Name: in.Name, Spec: spec}
	if err := s.DB.CreateImage(r.Context(), img); err != nil {
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
		spec.Access = model.Access{}
		if !p.Admin {
			spec.Prepull = img.Spec.Prepull
		}
	}
	img.Spec = spec
	if err := s.DB.UpdateImage(r.Context(), img); err != nil {
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
	if err := s.DB.DeleteImage(r.Context(), img.Name); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListClasses returns the classes the principal can use, default first.
func (s *Server) ListClasses(ctx context.Context, p *auth.Principal) ([]Class, error) {
	list := s.Catalog.Classes()
	out := make([]Class, 0, len(list))
	for i := range list {
		if access.CanUseClass(p, &list[i]) {
			out = append(out, toClass(&list[i]))
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

func (s *Server) loadClass(_ context.Context, p *auth.Principal, name string) (*model.Class, error) {
	c, err := s.Catalog.Class(name)
	if err != nil {
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
