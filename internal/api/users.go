package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// --- tokens (own) ---

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tokens.List(r.Context(), principal(r).Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in CreateTokenRequest
	if !decode(w, r, &in) {
		return
	}
	p := principal(r)
	if p.Method == "token" {
		fail(w, r, forbidden("tokens can not create tokens; sign in to the console"))
		return
	}
	if in.ExpiresInDays < 0 || in.ExpiresInDays > 3650 {
		fail(w, r, badRequest("expiresInDays must be between 0 and 3650"))
		return
	}
	existing, err := s.Tokens.List(r.Context(), p.Name)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(existing) >= 50 {
		fail(w, r, badRequest("at most 50 tokens per user; revoke unused ones"))
		return
	}
	t, plain, err := s.Tokens.Create(r.Context(), p.Name, in.Name, in.Scopes, time.Duration(in.ExpiresInDays)*24*time.Hour)
	if err != nil {
		fail(w, r, badRequest(err.Error()))
		return
	}
	writeJSON(w, http.StatusCreated, CreatedToken{Token: t, Value: plain})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if err := s.Tokens.Delete(r.Context(), principal(r).Name, r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- users ---

// listUsers returns user names for pickers (sharing). Everyone can list.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	list, err := s.Users.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	if p.Admin && r.URL.Query().Get("detail") != "" {
		out := make([]User, 0, len(list))
		for i := range list {
			out = append(out, s.toUser(&list[i], r.URL.Query().Get("usage") != "", r))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out := make([]UserSummary, 0, len(list))
	for _, u := range list {
		if u.Spec.Disabled {
			continue
		}
		out = append(out, UserSummary{Name: u.Name, DisplayName: u.Spec.DisplayName})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) toUser(u *devenvv1.DevUser, withUsage bool, r *http.Request) User {
	role := string(u.Spec.Role)
	if role == "" {
		role = string(devenvv1.RoleUser)
	}
	out := User{
		Name: u.Name, DisplayName: u.Spec.DisplayName, Email: u.Spec.Email,
		Role: role, Admin: s.Users.IsAdmin(u), Disabled: u.Spec.Disabled,
		Identities: toIdentities(u.Spec.Identities), Groups: u.Status.Groups,
		LastLoginProvider: u.Status.LastLoginProvider, SSHKeys: len(u.Spec.SSHKeys),
		CreatedAt: u.CreationTimestamp.Time,
		Local:     u.Name == s.Config.Auth.Password.Username,
		Limits: LimitsInput{
			MaxEnvironments: u.Spec.Limits.MaxEnvironments, MaxRunning: u.Spec.Limits.MaxRunning,
			CPU: qstr(u.Spec.Limits.CPU), Memory: qstr(u.Spec.Limits.Memory), Disk: qstr(u.Spec.Limits.Disk),
		},
	}
	if out.Groups == nil {
		out.Groups = []string{}
	}
	if u.Status.LastLoginAt != nil {
		t := u.Status.LastLoginAt.Time
		out.LastLoginAt = &t
	}
	if withUsage {
		if us, err := s.UserUsage(r.Context(), u.Name); err == nil {
			out.Usage = us
		}
	}
	return out
}

func fromLimitsInput(in LimitsInput) (devenvv1.UserLimits, error) {
	out := devenvv1.UserLimits{MaxEnvironments: in.MaxEnvironments, MaxRunning: in.MaxRunning}
	for _, n := range []*int32{in.MaxEnvironments, in.MaxRunning} {
		if n != nil && *n < 0 {
			return out, badRequest("limits must not be negative")
		}
	}
	parse := func(field, v string) (*resource.Quantity, error) {
		if v == "" {
			return nil, nil
		}
		q, err := resource.ParseQuantity(v)
		if err != nil || q.Sign() < 0 {
			return nil, badRequest(fmt.Sprintf("invalid %s limit %q", field, v))
		}
		return &q, nil
	}
	var err error
	if out.CPU, err = parse("cpu", in.CPU); err != nil {
		return out, err
	}
	if out.Memory, err = parse("memory", in.Memory); err != nil {
		return out, err
	}
	if out.Disk, err = parse("disk", in.Disk); err != nil {
		return out, err
	}
	return out, nil
}

func validRole(r string) bool { return r == "" || r == "admin" || r == "user" }

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in CreateUserRequest
	if !decode(w, r, &in) {
		return
	}
	if errs := validation.IsDNS1123Label(in.Name); len(errs) > 0 || len(in.Name) > 40 {
		fail(w, r, badRequest("name must be a DNS label of at most 40 characters"))
		return
	}
	if !validRole(in.Role) {
		fail(w, r, badRequest("role must be admin or user"))
		return
	}
	u := &devenvv1.DevUser{
		ObjectMeta: metav1.ObjectMeta{Name: in.Name, Labels: map[string]string{"app.kubernetes.io/managed-by": "devenv"}},
		Spec: devenvv1.DevUserSpec{
			DisplayName: strings.TrimSpace(in.DisplayName), Email: strings.TrimSpace(in.Email),
			Role: devenvv1.Role(in.Role),
		},
	}
	if u.Spec.Role == "" {
		u.Spec.Role = devenvv1.RoleUser
	}
	if in.Limits != nil {
		l, err := fromLimitsInput(*in.Limits)
		if err != nil {
			fail(w, r, err)
			return
		}
		u.Spec.Limits = l
	}
	if err := s.Client.Create(r.Context(), u); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.toUser(u, false, r))
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	u, err := s.Users.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toUser(u, true, r))
}

func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in UpdateUserRequest
	if !decode(w, r, &in) {
		return
	}
	p := principal(r)
	u, err := s.Users.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	before := u.DeepCopy()
	if in.DisplayName != nil {
		u.Spec.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Email != nil {
		u.Spec.Email = strings.TrimSpace(*in.Email)
	}
	if in.Role != nil {
		if !validRole(*in.Role) {
			fail(w, r, badRequest("role must be admin or user"))
			return
		}
		if u.Name == p.Name && *in.Role != "admin" && u.IsAdmin() {
			fail(w, r, badRequest("you can not remove your own admin role"))
			return
		}
		if u.Name == s.Config.Auth.Password.Username && *in.Role != "admin" {
			fail(w, r, badRequest("the local account is always an administrator"))
			return
		}
		u.Spec.Role = devenvv1.Role(*in.Role)
	}
	if in.Disabled != nil {
		if u.Name == p.Name && *in.Disabled {
			fail(w, r, badRequest("you can not disable yourself"))
			return
		}
		u.Spec.Disabled = *in.Disabled
	}
	if in.Limits != nil {
		l, err := fromLimitsInput(*in.Limits)
		if err != nil {
			fail(w, r, err)
			return
		}
		u.Spec.Limits = l
	}
	if in.Identities != nil {
		ids := []devenvv1.Identity{}
		for _, id := range *in.Identities {
			if s.Config.Provider(id.Provider) == nil || id.Subject == "" {
				fail(w, r, badRequest(fmt.Sprintf("unknown provider %q or empty subject", id.Provider)))
				return
			}
			ids = append(ids, devenvv1.Identity{Provider: id.Provider, Subject: id.Subject})
		}
		u.Spec.Identities = ids
	}
	if err := s.Client.Patch(r.Context(), u, client.MergeFrom(before)); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toUser(u, true, r))
}

// adminDeleteUser deletes a user and their tokens. The user's environments
// and personal images are kept unless ?purge=1 (then they are deleted).
func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	p := principal(r)
	name := r.PathValue("name")
	if name == p.Name || name == s.Config.Auth.Password.Username {
		fail(w, r, badRequest("this user can not be deleted"))
		return
	}
	u, err := s.Users.Get(r.Context(), name)
	if err != nil {
		fail(w, r, err)
		return
	}
	ctx := r.Context()
	if r.URL.Query().Get("purge") != "" {
		envs := &devenvv1.DevEnvironmentList{}
		if err := s.Client.List(ctx, envs, client.InNamespace(s.Namespace)); err != nil {
			fail(w, r, err)
			return
		}
		for i := range envs.Items {
			if envs.Items[i].Spec.Owner == name {
				if err := s.DeleteEnvironment(ctx, p, envs.Items[i].Name); client.IgnoreNotFound(err) != nil {
					fail(w, r, err)
					return
				}
			}
		}
		imgs := &devenvv1.DevImageList{}
		if err := s.Client.List(ctx, imgs); err != nil {
			fail(w, r, err)
			return
		}
		for i := range imgs.Items {
			if imgs.Items[i].Spec.Owner == name {
				_ = s.Client.Delete(ctx, &imgs.Items[i])
			}
		}
	}
	if err := s.Tokens.DeleteUser(ctx, name); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.Client.Delete(ctx, u, client.Preconditions{UID: &u.UID}); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminListTokens(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	out, err := s.Tokens.List(r.Context(), r.URL.Query().Get("user"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) adminDeleteToken(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if err := s.Tokens.Delete(r.Context(), "", r.PathValue("id")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminActivity(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.Activity.List(r.URL.Query().Get("environment"), 1000, nil))
}

func (s *Server) adminSystem(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx := r.Context()
	out := System{Version: s.Version, Namespace: s.Namespace, Config: s.Config.Redacted(), SSH: s.SSH, Counts: map[string]int{}}
	out.Helper = s.HelperImage
	if c, err := s.Capacity.Report(ctx, &devenvv1.DevEnvironmentClassSpec{}, nil); err == nil {
		out.Nodes = c.Nodes
	}
	if out.Nodes == nil {
		out.Nodes = []capacityNode{}
	}
	envs := &devenvv1.DevEnvironmentList{}
	if err := s.Client.List(ctx, envs, client.InNamespace(s.Namespace)); err == nil {
		out.Counts["environments"] = len(envs.Items)
		for _, e := range envs.Items {
			if e.Status.Phase == devenvv1.PhaseRunning {
				out.Counts["running"]++
			}
		}
	}
	if us, err := s.Users.List(ctx); err == nil {
		out.Counts["users"] = len(us)
	}
	if ts, err := s.Tokens.List(ctx, ""); err == nil {
		out.Counts["tokens"] = len(ts)
	}
	imgs := &devenvv1.DevImageList{}
	if err := s.Client.List(ctx, imgs); err == nil {
		out.Counts["images"] = len(imgs.Items)
	}
	writeJSON(w, http.StatusOK, out)
}
