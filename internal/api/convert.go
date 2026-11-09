package api

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/tinyrack-net/devenv/internal/model"
)

func qstr(q *resource.Quantity) string {
	if q == nil || q.IsZero() {
		return ""
	}
	return q.String()
}

func toResourcesInput(r model.Resources) ResourcesInput {
	return ResourcesInput{CPU: qstr(r.CPU), Memory: qstr(r.Memory), Disk: qstr(r.Disk)}
}

func parseQuantity(field, v string) (*resource.Quantity, error) {
	if v == "" {
		return nil, nil
	}
	q, err := resource.ParseQuantity(v)
	if err != nil {
		return nil, badRequest(fmt.Sprintf("invalid %s %q", field, v))
	}
	if q.Sign() <= 0 {
		return nil, badRequest(fmt.Sprintf("%s must be positive", field))
	}
	return &q, nil
}

func fromResourcesInput(in ResourcesInput) (model.Resources, error) {
	var out model.Resources
	var err error
	if out.CPU, err = parseQuantity("cpu", in.CPU); err != nil {
		return out, err
	}
	if out.Memory, err = parseQuantity("memory", in.Memory); err != nil {
		return out, err
	}
	if out.Disk, err = parseQuantity("disk", in.Disk); err != nil {
		return out, err
	}
	return out, nil
}

func toImage(i *model.Image, inUse int) Image {
	return Image{
		Name:             i.Name,
		Image:            i.Spec.Image,
		DisplayName:      i.Spec.DisplayName,
		Description:      i.Spec.Description,
		Desktop:          i.Spec.Desktop,
		Disabled:         i.Spec.Disabled,
		Prepull:          i.Spec.Prepull,
		Owner:            i.Spec.Owner,
		Access:           toAccessInput(i.Spec.Access),
		ImagePullPolicy:  string(i.Spec.ImagePullPolicy),
		DefaultResources: toResourcesInput(i.Spec.DefaultResources),
		CreatedAt:        i.CreatedAt,
		InUse:            inUse,
		Managed:          i.Managed,
	}
}

func toClass(c *model.Class) Class {
	out := Class{
		Name:        c.Name,
		StorageMode: string(c.Spec.EffectiveStorageMode()),
		DefaultDisk: qstr(c.Spec.Storage.DefaultSize),
		NetworkMode: string(c.Spec.EffectiveNetworkMode()),
		Limits: ResourcesInput{
			CPU:    qstr(c.Spec.Limits.MaxCPU),
			Memory: qstr(c.Spec.Limits.MaxMemory),
			Disk:   qstr(c.Spec.Limits.MaxDisk),
		},
		Default: c.Name == model.DefaultClassName,
		Access:  toAccessInput(c.Spec.Access),
	}
	if c.Spec.Storage.StorageClassName != nil {
		out.StorageClass = *c.Spec.Storage.StorageClassName
	}
	return out
}

func toEnvironment(e *model.Environment) Environment {
	keys := e.Spec.SSHAuthorizedKeys
	if keys == nil {
		keys = []string{}
	}
	phase := string(e.Status.Phase)
	if phase == "" {
		phase = string(model.PhasePending)
	}
	return Environment{
		Name:        e.Name,
		Owner:       e.Spec.Owner,
		Image:       e.Spec.ImageRef,
		ImageRef:    e.Status.Image,
		Class:       e.ClassName(),
		Resources:   toResourcesInput(e.Spec.Resources),
		Effective:   toResourcesInput(e.Status.Resources),
		Running:     e.IsRunning(),
		Phase:       phase,
		Message:     e.Status.Message,
		Desktop:     e.Status.Desktop,
		NodeName:    e.Status.NodeName,
		IP:          e.Status.IP,
		MAC:         e.Status.MAC,
		NetworkMode: string(e.Status.NetworkMode),
		Ports:       toPortInputs(e.Spec.Ports),
		PasswordSet: e.PasswordHash != "",
		SSHKeys:     keys,
		CreatedAt:   e.CreatedAt,
		Deleting:    e.Deleting,
		ServiceName: e.Status.ServiceName,
		Sharing:     toAccessInput(e.Spec.Sharing),
		CreatedBy:   e.CreatedBy,
		Reason:      e.Status.Reason,
	}
}

func toAccessInput(a model.Access) AccessInput {
	out := AccessInput{Users: a.Users, Groups: a.Groups}
	if out.Users == nil {
		out.Users = []string{}
	}
	if out.Groups == nil {
		out.Groups = []string{}
	}
	return out
}

func toPortInputs(ps []model.Port) []PortInput {
	out := make([]PortInput, 0, len(ps))
	for _, p := range ps {
		out = append(out, PortInput{Port: p.Port, Protocol: p.Protocol, Name: p.Name})
	}
	return out
}

func fromPortInputs(in []PortInput) ([]model.Port, error) {
	if len(in) > 64 {
		return nil, badRequest("at most 64 ports")
	}
	seen := map[string]bool{}
	out := make([]model.Port, 0, len(in))
	for _, p := range in {
		proto := p.Protocol
		if proto == "" {
			proto = "TCP"
		}
		if proto != "TCP" && proto != "UDP" {
			return nil, badRequest(fmt.Sprintf("invalid protocol %q", p.Protocol))
		}
		if p.Port < 1 || p.Port > 65535 {
			return nil, badRequest(fmt.Sprintf("invalid port %d", p.Port))
		}
		key := fmt.Sprintf("%s/%d", proto, p.Port)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model.Port{Port: p.Port, Protocol: proto, Name: p.Name})
	}
	return out, nil
}
