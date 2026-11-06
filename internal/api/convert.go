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
		SSHKeys:     keys,
		CreatedAt:   e.CreationTimestamp.Time,
		Deleting:    !e.DeletionTimestamp.IsZero(),
		ServiceName: e.Status.ServiceName,
	}
}
