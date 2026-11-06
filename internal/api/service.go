package api

import (
	"context"
	"errors"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// Service functions shared with other frontends (MCP).

// ToEnvironment converts an environment to its API representation.
func ToEnvironment(e *devenvv1.DevEnvironment) Environment { return toEnvironment(e) }

// ErrorMessage returns a user facing message and whether the error was caused by the request.
func ErrorMessage(err error) (msg string, userError bool) {
	var he *httpError
	switch {
	case errors.As(err, &he):
		return he.msg, he.code < 500
	case apierrors.IsNotFound(err):
		return "not found", true
	case apierrors.IsAlreadyExists(err):
		return "already exists", true
	case apierrors.IsConflict(err):
		return "resource was modified concurrently, retry", true
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err), apierrors.IsForbidden(err):
		return err.Error(), true
	}
	return err.Error(), false
}

// ListImages returns the image catalog with usage counts.
func (s *Server) ListImages(ctx context.Context) ([]Image, error) {
	list := &devenvv1.DevImageList{}
	if err := s.Client.List(ctx, list); err != nil {
		return nil, err
	}
	envs := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(ctx, envs, client.InNamespace(s.Namespace)); err != nil {
		return nil, err
	}
	usage := map[string]int{}
	for _, e := range envs.Items {
		usage[e.Spec.ImageRef]++
	}
	out := make([]Image, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toImage(&list.Items[i], usage[list.Items[i].Name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListClasses returns the environment classes, default first.
func (s *Server) ListClasses(ctx context.Context) ([]Class, error) {
	list := &devenvv1.DevEnvironmentClassList{}
	if err := s.Client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]Class, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, toClass(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ClassCapacity returns what can currently be requested in a class.
func (s *Server) ClassCapacity(ctx context.Context, className, exclude string) (*Capacity, error) {
	class := &devenvv1.DevEnvironmentClass{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: className}, class); err != nil {
		return nil, err
	}
	var ex *client.ObjectKey
	if exclude != "" {
		ex = &client.ObjectKey{Namespace: s.Namespace, Name: exclude}
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
