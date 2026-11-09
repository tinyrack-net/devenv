package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/catalog"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
	"github.com/tinyrack-net/devenv/internal/users"
)

// maxNameLength leaves room for child object suffixes such as "-home".
const maxNameLength = 40

// CreatedByMCP marks environments created by agents.
const CreatedByMCP = "mcp"

func validateName(name string) error {
	if len(name) > maxNameLength {
		return badRequest(fmt.Sprintf("name must be at most %d characters", maxNameLength))
	}
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return badRequest("invalid name: " + errs[0])
	}
	return nil
}

func (s *Server) view(e *model.Environment, lvl access.Level) Environment {
	out := toEnvironment(e)
	out.Access = lvl.String()
	return out
}

// ListEnvironments returns the environments the principal can access, sorted by name.
func (s *Server) ListEnvironments(ctx context.Context, p *auth.Principal) ([]model.Environment, error) {
	list, err := s.DB.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, e := range list {
		if access.Environment(p, &e) != access.None {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	// By default everyone (admins too) sees their own and shared
	// environments; admins pass ?all=1 to see every user's environments.
	// ?owner=<name> filters by owner.
	p := principal(r)
	q := r.URL.Query()
	envs, err := s.ListEnvironments(r.Context(), p)
	if err != nil {
		fail(w, r, err)
		return
	}
	personal := &auth.Principal{Name: p.Name, Groups: p.Groups}
	out := make([]Environment, 0, len(envs))
	for i := range envs {
		e := &envs[i]
		if o := q.Get("owner"); o != "" && e.Spec.Owner != o {
			continue
		}
		if q.Get("all") == "" && q.Get("owner") == "" && access.Environment(personal, e) == access.None {
			continue
		}
		out = append(out, s.view(e, access.Environment(p, e)))
	}
	writeJSON(w, http.StatusOK, out)
}

// Authorize loads an environment and checks the principal's access level.
// Environments without access are reported as not found.
func (s *Server) Authorize(ctx context.Context, p *auth.Principal, name string, need access.Level) (*model.Environment, access.Level, error) {
	env, err := s.DB.GetEnvironment(ctx, name)
	if err != nil {
		return nil, access.None, err
	}
	lvl := access.Environment(p, env)
	if lvl == access.None {
		return nil, lvl, store.ErrNotFound
	}
	if lvl < need {
		return nil, lvl, forbidden("only the owner of the environment or an administrator can do this")
	}
	return env, lvl, nil
}

func (s *Server) getEnvironment(w http.ResponseWriter, r *http.Request) {
	env, lvl, err := s.Authorize(r.Context(), principal(r), r.PathValue("name"), access.Use)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(env, lvl))
}

// admit validates an environment spec against its image, class, the owner's
// limits and cluster capacity.
func (s *Server) admit(ctx context.Context, p *auth.Principal, env *model.Environment, newImage, checkCapacity bool) error {
	img, err := s.Catalog.Image(ctx, env.Spec.ImageRef)
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			return badRequest(fmt.Sprintf("image %q not found", env.Spec.ImageRef))
		}
		return err
	}
	if newImage {
		if img.Spec.Disabled {
			return badRequest(fmt.Sprintf("image %q is disabled", env.Spec.ImageRef))
		}
		if !access.CanUseImage(p, img) {
			return badRequest(fmt.Sprintf("image %q not found", env.Spec.ImageRef))
		}
	}
	class, err := s.Catalog.Class(env.ClassName())
	if err != nil {
		return badRequest(fmt.Sprintf("class %q not found", env.ClassName()))
	}
	if env.CreatedAt.IsZero() && !access.CanUseClass(p, class) {
		return badRequest(fmt.Sprintf("class %q not found", env.ClassName()))
	}
	res := model.Resolve(env.Spec.Resources, &img.Spec, &class.Spec)
	if err := class.Spec.CheckLimits(res); err != nil {
		return badRequest(err.Error())
	}
	if err := s.checkUserLimits(ctx, env, res); err != nil {
		return err
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

// CreateOptions are extra settings applied by callers other than the console.
type CreateOptions struct {
	// CreatedBy is recorded on the environment ("mcp").
	CreatedBy string
}

func fromAccessInput(in *AccessInput) (model.Access, error) {
	if in == nil {
		return model.Access{}, nil
	}
	clean := func(xs []string, kind string, user bool) ([]string, error) {
		var out []string
		for _, x := range xs {
			x = strings.TrimSpace(x)
			if x == "" || slices.Contains(out, x) {
				continue
			}
			if user {
				if errs := validation.IsDNS1123Label(x); len(errs) > 0 {
					return nil, badRequest(fmt.Sprintf("invalid %s %q", kind, x))
				}
			} else if len(x) > 253 {
				return nil, badRequest(fmt.Sprintf("invalid %s %q", kind, x))
			}
			out = append(out, x)
		}
		if len(out) > 100 {
			return nil, badRequest("at most 100 " + kind + "s")
		}
		sort.Strings(out)
		return out, nil
	}
	u, err := clean(in.Users, "user", true)
	if err != nil {
		return model.Access{}, err
	}
	g, err := clean(in.Groups, "group", false)
	if err != nil {
		return model.Access{}, err
	}
	return model.Access{Users: u, Groups: g}, nil
}

// resolveOwner returns the owner of a new environment.
func (s *Server) resolveOwner(ctx context.Context, p *auth.Principal, requested string) (string, error) {
	if requested == "" || requested == p.Name {
		return p.Name, nil
	}
	if !p.Admin {
		return "", forbidden("only administrators can create environments for other users")
	}
	if _, err := s.Users.Get(ctx, requested); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", badRequest(fmt.Sprintf("user %q not found", requested))
		}
		return "", err
	}
	return requested, nil
}

// CreateEnvironment validates and creates an environment for the principal.
func (s *Server) CreateEnvironment(ctx context.Context, p *auth.Principal, in CreateEnvironmentRequest, opts CreateOptions) (*model.Environment, error) {
	if err := validateName(in.Name); err != nil {
		return nil, err
	}
	if in.Image == "" {
		return nil, badRequest("image is required")
	}
	owner, err := s.resolveOwner(ctx, p, in.Owner)
	if err != nil {
		return nil, err
	}
	res, err := fromResourcesInput(in.Resources)
	if err != nil {
		return nil, err
	}
	ports, err := fromPortInputs(in.Ports)
	if err != nil {
		return nil, err
	}
	sharing, err := fromAccessInput(in.Sharing)
	if err != nil {
		return nil, err
	}
	if in.Password != "" {
		if err := validatePassword(in.Password); err != nil {
			return nil, err
		}
	}
	var keys []string
	if in.SSHKeys != nil {
		if keys, err = normalizeKeys(*in.SSHKeys); err != nil {
			return nil, err
		}
	}
	env := &model.Environment{
		Name:      in.Name,
		CreatedBy: opts.CreatedBy,
		Spec: model.EnvironmentSpec{
			Owner:             owner,
			Sharing:           sharing,
			ImageRef:          in.Image,
			ClassName:         in.Class,
			Resources:         res,
			Running:           ptr.To(in.Running == nil || *in.Running),
			SSHAuthorizedKeys: keys,
			Ports:             ports,
		},
	}
	if err := s.admit(ctx, p, env, true, true); err != nil {
		return nil, err
	}
	if in.Password != "" {
		hash, err := controller.HashPassword(in.Password)
		if err != nil {
			return nil, err
		}
		env.PasswordHash = hash
	}
	if err := s.DB.CreateEnvironment(ctx, env); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var in CreateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	env, err := s.CreateEnvironment(r.Context(), principal(r), in, CreateOptions{})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.view(env, access.Manage))
}

// UpdateEnvironment applies a partial update. Starting and stopping only need
// use access; everything else needs manage access.
func (s *Server) UpdateEnvironment(ctx context.Context, p *auth.Principal, name string, in UpdateEnvironmentRequest) (*model.Environment, error) {
	need := access.Manage
	if in.Image == nil && in.Class == nil && in.Resources == nil && in.SSHKeys == nil && in.Ports == nil && in.Password == nil && in.Sharing == nil && in.Owner == nil {
		need = access.Use
	}
	env, _, err := s.Authorize(ctx, p, name, need)
	if err != nil {
		return nil, err
	}
	before := env.DeepCopy()
	if in.Image != nil {
		env.Spec.ImageRef = *in.Image
	}
	classChanged := false
	if in.Class != nil && *in.Class != "" && *in.Class != env.ClassName() {
		if err := s.checkClassChange(ctx, p, env, *in.Class); err != nil {
			return nil, err
		}
		env.Spec.ClassName = *in.Class
		classChanged = true
	}
	if in.Resources != nil {
		res, err := fromResourcesInput(*in.Resources)
		if err != nil {
			return nil, err
		}
		env.Spec.Resources = res
	}
	if in.SSHKeys != nil {
		keys, err := normalizeKeys(*in.SSHKeys)
		if err != nil {
			return nil, err
		}
		env.Spec.SSHAuthorizedKeys = keys
	}
	if in.Running != nil {
		env.Spec.Running = ptr.To(*in.Running)
	}
	if in.Ports != nil {
		ports, err := fromPortInputs(*in.Ports)
		if err != nil {
			return nil, err
		}
		env.Spec.Ports = ports
	}
	if in.Sharing != nil {
		sharing, err := fromAccessInput(in.Sharing)
		if err != nil {
			return nil, err
		}
		env.Spec.Sharing = sharing
	}
	if in.Owner != nil && *in.Owner != env.Spec.Owner {
		if !p.Admin {
			return nil, forbidden("only administrators can transfer environments")
		}
		owner, err := s.resolveOwner(ctx, p, *in.Owner)
		if err != nil {
			return nil, err
		}
		env.Spec.Owner = owner
	}

	pushHash := ""
	if in.Password != nil {
		if *in.Password == "" {
			env.PasswordHash = ""
		} else {
			if err := validatePassword(*in.Password); err != nil {
				return nil, err
			}
			hash, err := controller.HashPassword(*in.Password)
			if err != nil {
				return nil, err
			}
			pushHash = hash
			env.PasswordHash = hash
		}
	}

	imageChanged := env.Spec.ImageRef != before.Spec.ImageRef
	needsCapacity := imageChanged || classChanged || in.Resources != nil || (env.IsRunning() && !before.IsRunning())
	if err := s.admit(ctx, p, env, imageChanged, needsCapacity); err != nil {
		return nil, err
	}
	if err := s.DB.UpdateEnvironment(ctx, env); err != nil {
		return nil, err
	}
	if in.Password != nil {
		s.pushPassword(env, pushHash)
	}
	return env, nil
}

func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	var in UpdateEnvironmentRequest
	if !decode(w, r, &in) {
		return
	}
	p := principal(r)
	env, err := s.UpdateEnvironment(r.Context(), p, r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(env, access.Environment(p, env)))
}

// SetRunning starts or stops an environment.
func (s *Server) SetRunning(ctx context.Context, p *auth.Principal, name string, running bool) (*model.Environment, error) {
	env, _, err := s.Authorize(ctx, p, name, access.Use)
	if err != nil {
		return nil, err
	}
	if env.IsRunning() == running {
		return env, nil
	}
	env.Spec.Running = ptr.To(running)
	if running {
		if err := s.admit(ctx, p, env, false, true); err != nil {
			return nil, err
		}
	}
	if err := s.DB.UpdateEnvironment(ctx, env); err != nil {
		return nil, err
	}
	return env, nil
}

func (s *Server) setRunning(w http.ResponseWriter, r *http.Request, running bool) {
	p := principal(r)
	env, err := s.SetRunning(r.Context(), p, r.PathValue("name"), running)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(env, access.Environment(p, env)))
}

func (s *Server) startEnvironment(w http.ResponseWriter, r *http.Request) { s.setRunning(w, r, true) }
func (s *Server) stopEnvironment(w http.ResponseWriter, r *http.Request)  { s.setRunning(w, r, false) }

// Restart deletes the environment pod; the controller recreates it.
func (s *Server) Restart(ctx context.Context, p *auth.Principal, name string) (*model.Environment, error) {
	env, _, err := s.Authorize(ctx, p, name, access.Use)
	if err != nil {
		return nil, err
	}
	if !env.IsRunning() {
		return nil, badRequest("environment is stopped; start it instead")
	}
	pod := &corev1.Pod{}
	if err := s.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			return env, nil
		}
		return nil, err
	}
	if s.Pool != nil {
		s.Pool.Forget(name)
	}
	if s.Client == nil {
		return nil, errors.New("restart unavailable")
	}
	if err := s.Client.Delete(ctx, pod, client.Preconditions{UID: &pod.UID}); client.IgnoreNotFound(err) != nil {
		return nil, err
	}
	return env, nil
}

func (s *Server) restartEnvironment(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	env, err := s.Restart(r.Context(), p, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.view(env, access.Environment(p, env)))
}

// DeleteEnvironment marks an environment for deletion. The controller
// removes its pod, home volume and secrets, then the record.
func (s *Server) DeleteEnvironment(ctx context.Context, p *auth.Principal, name string) error {
	if _, _, err := s.Authorize(ctx, p, name, access.Manage); err != nil {
		return err
	}
	_, err := s.DB.MutateEnvironment(ctx, name, func(e *model.Environment) error {
		e.Deleting = true
		e.Spec.Running = ptr.To(false)
		return nil
	})
	return err
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if err := s.DeleteEnvironment(r.Context(), principal(r), r.PathValue("name")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// GetEnvironment returns an environment the principal can use.
func (s *Server) GetEnvironment(ctx context.Context, p *auth.Principal, name string) (*model.Environment, error) {
	env, _, err := s.Authorize(ctx, p, name, access.Use)
	return env, err
}

// +kubebuilder:rbac:groups="",resources=events,verbs=list

func (s *Server) environmentEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, _, err := s.Authorize(r.Context(), principal(r), name, access.Use); err != nil {
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
		out = append(out, Event{
			Type:    e.Type,
			Reason:  e.Reason,
			Message: strings.TrimSpace(e.Message),
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

func (s *Server) environmentActivity(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, _, err := s.Authorize(r.Context(), principal(r), name, access.Use); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.Activity.List(name, 300, nil))
}

// --- user limits ---

type consumption struct {
	envs, running int32
	cpu, mem      resource.Quantity
	disk          resource.Quantity
}

// usage sums the resources of a user's environments. skip excludes one
// environment (the one being admitted).
func (s *Server) usage(ctx context.Context, owner, skip string) (consumption, error) {
	var c consumption
	list, err := s.DB.ListEnvironments(ctx)
	if err != nil {
		return c, err
	}
	for i := range list {
		e := &list[i]
		if e.Spec.Owner != owner || e.Name == skip || e.Deleting {
			continue
		}
		res := s.resolveResources(ctx, e)
		c.envs++
		c.disk.Add(res.Disk)
		if e.IsRunning() {
			c.running++
			c.cpu.Add(res.CPU)
			c.mem.Add(res.Memory)
		}
	}
	return c, nil
}

func (s *Server) resolveResources(ctx context.Context, e *model.Environment) model.EffectiveResources {
	img, err := s.Catalog.Image(ctx, e.Spec.ImageRef)
	if err != nil {
		img = &model.Image{}
	}
	cl, err := s.Catalog.Class(e.ClassName())
	if err != nil {
		cl = &model.Class{}
	}
	res := model.Resolve(e.Spec.Resources, &img.Spec, &cl.Spec)
	if cl.Spec.EffectiveStorageMode() == model.StorageModeEphemeral {
		res.Disk = resource.Quantity{}
	}
	return res
}

func (s *Server) ownerLimits(ctx context.Context, owner string) (*model.User, bool, error) {
	u, err := s.Users.Get(ctx, owner)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return u, s.Users.IsAdmin(u), nil
}

// checkUserLimits enforces the owner's limits for a new or changed environment.
func (s *Server) checkUserLimits(ctx context.Context, env *model.Environment, res model.EffectiveResources) error {
	u, admin, err := s.ownerLimits(ctx, env.Spec.Owner)
	if err != nil {
		return err
	}
	lim := s.Users.EffectiveLimits(u, admin)
	if lim == (users.Limits{}) {
		return nil
	}
	c, err := s.usage(ctx, env.Spec.Owner, env.Name)
	if err != nil {
		return err
	}
	exceeded := func(what string, used, limit resource.Quantity) error {
		return &httpError{http.StatusUnprocessableEntity, fmt.Sprintf("%s limit exceeded for user %s: %s would be used of %s", what, env.Spec.Owner, used.String(), limit.String())}
	}
	if lim.MaxEnvironments != nil && c.envs+1 > *lim.MaxEnvironments {
		return &httpError{http.StatusUnprocessableEntity, fmt.Sprintf("user %s can own at most %d environments", env.Spec.Owner, *lim.MaxEnvironments)}
	}
	disk := c.disk.DeepCopy()
	disk.Add(res.Disk)
	if lim.Disk != nil && disk.Cmp(*lim.Disk) > 0 {
		return exceeded("disk", disk, *lim.Disk)
	}
	if !env.IsRunning() {
		return nil
	}
	if lim.MaxRunning != nil && c.running+1 > *lim.MaxRunning {
		return &httpError{http.StatusUnprocessableEntity, fmt.Sprintf("user %s can run at most %d environments at once; stop one first", env.Spec.Owner, *lim.MaxRunning)}
	}
	cpu := c.cpu.DeepCopy()
	cpu.Add(res.CPU)
	if lim.CPU != nil && cpu.Cmp(*lim.CPU) > 0 {
		return exceeded("CPU", cpu, *lim.CPU)
	}
	mem := c.mem.DeepCopy()
	mem.Add(res.Memory)
	if lim.Memory != nil && mem.Cmp(*lim.Memory) > 0 {
		return exceeded("memory", mem, *lim.Memory)
	}
	return nil
}

// UserUsage returns a user's consumption and limits.
func (s *Server) UserUsage(ctx context.Context, name string) (*Usage, error) {
	u, admin, err := s.ownerLimits(ctx, name)
	if err != nil {
		return nil, err
	}
	c, err := s.usage(ctx, name, "")
	if err != nil {
		return nil, err
	}
	out := &Usage{Environments: c.envs, Running: c.running, CPU: c.cpu.String(), Memory: c.mem.String(), Disk: c.disk.String()}
	lim := s.Users.EffectiveLimits(u, admin)
	out.Limits.MaxEnvironments = lim.MaxEnvironments
	out.Limits.MaxRunning = lim.MaxRunning
	out.Limits.CPU = qstr(lim.CPU)
	out.Limits.Memory = qstr(lim.Memory)
	out.Limits.Disk = qstr(lim.Disk)
	return out, nil
}

func (s *Server) myUsage(w http.ResponseWriter, r *http.Request) {
	out, err := s.UserUsage(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// checkClassChange validates moving an environment to another class: the
// caller may use the target class and the home volume stays where it is.
func (s *Server) checkClassChange(ctx context.Context, p *auth.Principal, env *model.Environment, to string) error {
	target, err := s.Catalog.Class(to)
	if err != nil {
		return badRequest(fmt.Sprintf("class %q not found", to))
	}
	if !access.CanUseClass(p, target) {
		return badRequest(fmt.Sprintf("class %q not found", to))
	}
	current, err := s.Catalog.Class(env.ClassName())
	if err != nil {
		// The old class is gone; nothing to compare with.
		return nil
	}
	if err := model.StorageCompatible(&current.Spec, &target.Spec); err != nil {
		return badRequest(fmt.Sprintf("can not move to class %q: %v; the home volume can not move between storage types", to, err))
	}
	return nil
}
