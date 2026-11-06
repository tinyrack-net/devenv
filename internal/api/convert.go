package api

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

func qstr(q *resource.Quantity) string {
	if q == nil || q.IsZero() {
		return ""
	}
	return q.String()
}

func toResourcesInput(r devenvv1.Resources) ResourcesInput {
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

func fromResourcesInput(in ResourcesInput) (devenvv1.Resources, error) {
	var out devenvv1.Resources
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

func toImage(i *devenvv1.DevImage, inUse int) Image {
	return Image{
		Name:             i.Name,
		Image:            i.Spec.Image,
		DisplayName:      i.Spec.DisplayName,
		Description:      i.Spec.Description,
		Desktop:          i.Spec.Desktop,
		Disabled:         i.Spec.Disabled,
		ImagePullPolicy:  string(i.Spec.ImagePullPolicy),
		DefaultResources: toResourcesInput(i.Spec.DefaultResources),
		CreatedAt:        i.CreationTimestamp.Time,
		InUse:            inUse,
	}
}

func toClass(c *devenvv1.DevEnvironmentClass) Class {
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
		Default: c.Name == devenvv1.DefaultClassName,
	}
	if c.Spec.Storage.StorageClassName != nil {
		out.StorageClass = *c.Spec.Storage.StorageClassName
	}
	return out
}

func toEnvironment(e *devenvv1.DevEnvironment) Environment {
	keys := e.Spec.SSHAuthorizedKeys
	if keys == nil {
		keys = []string{}
	}
	phase := string(e.Status.Phase)
	if phase == "" {
		phase = string(devenvv1.PhasePending)
	}
	return Environment{
		Name:        e.Name,
		Owner:       e.Spec.Owner,
		Image:       e.Spec.ImageRef,
		ImageRef:    e.Status.Image,
		Class:       e.ClassName(),
		Resources:   toResourcesInput(e.Spec.Resources),
		Running:     e.IsRunning(),
		Phase:       phase,
		Message:     e.Status.Message,
		Desktop:     e.Status.Desktop,
		NodeName:    e.Status.NodeName,
		IP:          e.Status.IP,
		MAC:         e.Status.MAC,
		NetworkMode: string(e.Status.NetworkMode),
		Ports:       toPortInputs(e.Spec.Ports),
		SSHKeys:     keys,
		CreatedAt:   e.CreationTimestamp.Time,
		Deleting:    !e.DeletionTimestamp.IsZero(),
		ServiceName: e.Status.ServiceName,
	}
}

func toPortInputs(ps []devenvv1.Port) []PortInput {
	out := make([]PortInput, 0, len(ps))
	for _, p := range ps {
		out = append(out, PortInput{Port: p.Port, Protocol: p.Protocol, Name: p.Name})
	}
	return out
}

func fromPortInputs(in []PortInput) ([]devenvv1.Port, error) {
	if len(in) > 64 {
		return nil, badRequest("at most 64 ports")
	}
	seen := map[string]bool{}
	out := make([]devenvv1.Port, 0, len(in))
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
		out = append(out, devenvv1.Port{Port: p.Port, Protocol: proto, Name: p.Name})
	}
	return out, nil
}
