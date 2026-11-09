// Package catalog resolves classes and images: classes and managed images
// come from the configuration file, console created images from the
// database. A database image can not shadow a configured one.
package catalog

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// ErrNotFound is returned for unknown classes and images.
var ErrNotFound = store.ErrNotFound

// Catalog combines configuration and database.
type Catalog struct {
	DB *store.Store

	mu      sync.RWMutex
	classes map[string]model.ClassSpec
	images  map[string]model.ImageSpec
}

// New returns a catalog with the classes and images of the configuration.
func New(cfg *config.Config, db *store.Store) *Catalog {
	c := &Catalog{DB: db, classes: map[string]model.ClassSpec{}, images: map[string]model.ImageSpec{}}
	for k, v := range cfg.Classes {
		c.classes[k] = v
	}
	for k, v := range cfg.Images {
		c.images[k] = v
	}
	return c
}

// SetClass adds or replaces a class (tests; configuration is static).
func (c *Catalog) SetClass(name string, spec model.ClassSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.classes[name] = spec
}

// SetImage adds or replaces a managed image (tests).
func (c *Catalog) SetImage(name string, spec model.ImageSpec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.images[name] = spec
}

// Class returns a class by name.
func (c *Catalog) Class(name string) (*model.Class, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	spec, ok := c.classes[name]
	if !ok {
		return nil, ErrNotFound
	}
	return model.DeepCopyClass(&model.Class{Name: name, Spec: spec}), nil
}

// Classes returns every class ordered by name ("default" first).
func (c *Catalog) Classes() []model.Class {
	c.mu.RLock()
	out := make([]model.Class, 0, len(c.classes))
	for name, spec := range c.classes {
		out = append(out, *model.DeepCopyClass(&model.Class{Name: name, Spec: spec}))
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Name == model.DefaultClassName) != (out[j].Name == model.DefaultClassName) {
			return out[i].Name == model.DefaultClassName
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Managed reports whether an image name belongs to the configuration.
func (c *Catalog) Managed(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.images[name]
	return ok
}

func (c *Catalog) managedImage(name string) (*model.Image, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	spec, ok := c.images[name]
	if !ok {
		return nil, false
	}
	return (&model.Image{Name: name, Managed: true, Spec: spec}).DeepCopy(), true
}

// Image returns an image by name.
func (c *Catalog) Image(ctx context.Context, name string) (*model.Image, error) {
	if img, ok := c.managedImage(name); ok {
		return img, nil
	}
	img, err := c.DB.GetImage(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return img, err
}

// Images returns every image ordered by name.
func (c *Catalog) Images(ctx context.Context) ([]model.Image, error) {
	stored, err := c.DB.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.RLock()
	out := make([]model.Image, 0, len(stored)+len(c.images))
	for name, spec := range c.images {
		out = append(out, *(&model.Image{Name: name, Managed: true, Spec: spec}).DeepCopy())
	}
	c.mu.RUnlock()
	for _, img := range stored {
		if !c.Managed(img.Name) {
			out = append(out, img)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
