package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// EffectiveResources is the fully defaulted resource set of an environment.
type EffectiveResources struct {
	CPU    resource.Quantity `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
	Disk   resource.Quantity `json:"disk"`
}

func firstQuantity(def string, qs ...*resource.Quantity) resource.Quantity {
	for _, q := range qs {
		if q != nil && !q.IsZero() {
			return q.DeepCopy()
		}
	}
	return resource.MustParse(def)
}

// Resolve merges environment, image and class defaults.
// Precedence: environment > image defaults > class defaults > built-in defaults.
func Resolve(env Resources, image *DevImageSpec, class *DevEnvironmentClassSpec) EffectiveResources {
	return EffectiveResources{
		CPU:    firstQuantity(DefaultCPU, env.CPU, image.DefaultResources.CPU),
		Memory: firstQuantity(DefaultMemory, env.Memory, image.DefaultResources.Memory),
		Disk:   firstQuantity(DefaultDisk, env.Disk, image.DefaultResources.Disk, class.Storage.DefaultSize),
	}
}

// EffectiveStorageMode returns the storage mode with defaults applied.
func (c *DevEnvironmentClassSpec) EffectiveStorageMode() StorageMode {
	if c.Storage.Mode == "" {
		return StorageModePersistent
	}
	return c.Storage.Mode
}

// EffectiveNetworkMode returns the network mode with defaults applied.
func (c *DevEnvironmentClassSpec) EffectiveNetworkMode() NetworkMode {
	if c.Network.Mode == "" {
		return NetworkModeGateway
	}
	return c.Network.Mode
}

// CheckLimits validates resources against the class limits.
func (c *DevEnvironmentClassSpec) CheckLimits(r EffectiveResources) error {
	check := func(name string, got resource.Quantity, max *resource.Quantity) error {
		if max != nil && got.Cmp(*max) > 0 {
			return fmt.Errorf("%s %s exceeds class limit %s", name, got.String(), max.String())
		}
		return nil
	}
	if err := check("cpu", r.CPU, c.Limits.MaxCPU); err != nil {
		return err
	}
	if err := check("memory", r.Memory, c.Limits.MaxMemory); err != nil {
		return err
	}
	if c.EffectiveStorageMode() == StorageModePersistent {
		if err := check("disk", r.Disk, c.Limits.MaxDisk); err != nil {
			return err
		}
	}
	return nil
}
