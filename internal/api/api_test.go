package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl test@example"

func qp(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

type harness struct {
	t      *testing.T
	srv    *Server
	h      http.Handler
	c      client.Client
	cookie *http.Cookie
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = devenvv1.AddToScheme(s)
	base := []client.Object{
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{
				Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("16Gi")},
				Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		},
		&devenvv1.DevEnvironmentClass{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Spec:       devenvv1.DevEnvironmentClassSpec{Limits: devenvv1.Limits{MaxCPU: qp("6"), MaxDisk: qp("100Gi")}},
		},
		&devenvv1.DevImage{
			ObjectMeta: metav1.ObjectMeta{Name: "base"},
			Spec:       devenvv1.DevImageSpec{Image: "example.com/base:1", DefaultResources: devenvv1.Resources{CPU: qp("2"), Memory: qp("4Gi")}},
		},
		&devenvv1.DevImage{
			ObjectMeta: metav1.ObjectMeta{Name: "old"},
			Spec:       devenvv1.DevImageSpec{Image: "example.com/old:1", Disabled: true},
		},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(append(base, objs...)...).
		WithStatusSubresource(&devenvv1.DevEnvironment{}, &devenvv1.DevUser{}).Build()
	sessions, _ := auth.NewSessions([]byte("k"), false)
	cfg := &config.Config{Auth: config.Auth{Password: config.Password{Password: "pw"}}}
	cfg.MCP.LegacyToken.Token = "legacy-token"
	cfg.Defaults()
	srv := &Server{
		Client:    c,
		Reader:    c,
		Namespace: "devenv",
		Config:    cfg,
		Sessions:  sessions,
		Users:     &users.Store{Client: c, Config: cfg},
		Tokens:    &tokens.Store{Client: c, Namespace: "devenv-system"},
		Capacity:  &capacity.Checker{Reader: c, Namespace: "devenv"},
		Activity:  &activity.Log{},
		Static:    fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}, "assets/app.js": {Data: []byte("js")}},
	}
	h := &harness{t: t, srv: srv, h: srv.Handler(), c: c}
	h.login()
	return h
}

// as returns a harness acting as another user (created when missing).
func (h *harness) as(name string, role devenvv1.Role, groups ...string) *harness {
	h.t.Helper()
	u := &devenvv1.DevUser{}
	if err := h.c.Get(h.t.Context(), client.ObjectKey{Name: name}, u); err != nil {
		u = &devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: devenvv1.DevUserSpec{Role: role}}
		if err := h.c.Create(h.t.Context(), u); err != nil {
			h.t.Fatal(err)
		}
	}
	if groups != nil {
		u.Status.Groups = groups
		if err := h.c.Status().Update(h.t.Context(), u); err != nil {
			h.t.Fatal(err)
		}
	}
	tok, _ := h.srv.Sessions.Encode(name, "test")
	return &harness{t: h.t, srv: h.srv, h: h.h, c: h.c, cookie: &http.Cookie{Name: "devenv_session", Value: tok}}
}

func (h *harness) login() {
	rec := h.raw("POST", "/api/v1/login", `{"username":"admin","password":"pw"}`, false)
	if rec.Code != 200 {
		h.t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "devenv_session" {
			h.cookie = c
		}
	}
	if h.cookie == nil {
		h.t.Fatal("no session cookie")
	}
}

func (h *harness) raw(method, path, body string, withCookie bool) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeader, "devenv")
	if withCookie && h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func (h *harness) do(method, path string, body any, wantCode int, out any) {
	h.t.Helper()
	var s string
	if body != nil {
		b, _ := json.Marshal(body)
		s = string(b)
	}
	rec := h.raw(method, path, s, true)
	if rec.Code != wantCode {
		h.t.Fatalf("%s %s: got %d want %d: %s", method, path, rec.Code, wantCode, rec.Body)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			h.t.Fatalf("decode: %v: %s", err, rec.Body)
		}
	}
}

func TestLoginRequired(t *testing.T) {
	h := newHarness(t)
	if rec := h.raw("GET", "/api/v1/environments", "", false); rec.Code != 401 {
		t.Errorf("unauthenticated: %d", rec.Code)
	}
	if rec := h.raw("POST", "/api/v1/login", `{"username":"admin","password":"bad"}`, false); rec.Code != 401 {
		t.Errorf("bad password: %d", rec.Code)
	}
	var info Info
	h.do("GET", "/api/v1/info", nil, 200, &info)
	if !info.Auth.Password || len(info.Providers) != 0 {
		t.Errorf("info = %+v", info)
	}
	var me Me
	h.do("GET", "/api/v1/me", nil, 200, &me)
	if me.Name != "admin" || !me.Admin || me.Method != "session" {
		t.Errorf("me = %+v", me)
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	h := newHarness(t)

	var key SSHKey
	h.do("POST", "/api/v1/sshkeys", map[string]string{"publicKey": testKey}, 201, &key)
	if key.Name != "test@example" || !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Errorf("key = %+v", key)
	}
	h.do("POST", "/api/v1/sshkeys", map[string]string{"publicKey": testKey}, 409, nil)

	var env Environment
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{
		Name: "work", Image: "base", Resources: ResourcesInput{Disk: "20Gi"},
	}, 201, &env)
	if env.Class != "default" || !env.Running || env.Owner != "admin" || len(env.SSHKeys) != 0 || env.Access != "manage" {
		t.Errorf("created = %+v", env)
	}

	obj := &devenvv1.DevEnvironment{}
	if err := h.c.Get(t.Context(), client.ObjectKey{Namespace: "devenv", Name: "work"}, obj); err != nil {
		t.Fatal(err)
	}
	if obj.Spec.Resources.Disk.String() != "20Gi" || obj.Labels[devenvv1.LabelOwner] != "admin" {
		t.Errorf("disk = %v labels = %v", obj.Spec.Resources.Disk, obj.Labels)
	}

	var list []Environment
	h.do("GET", "/api/v1/environments", nil, 200, &list)
	if len(list) != 1 {
		t.Errorf("list len = %d", len(list))
	}

	h.do("POST", "/api/v1/environments/work/stop", nil, 200, &env)
	if env.Running {
		t.Errorf("should be stopped")
	}
	h.do("POST", "/api/v1/environments/work/start", nil, 200, &env)
	if !env.Running {
		t.Errorf("should be running")
	}

	h.do("PATCH", "/api/v1/environments/work", UpdateEnvironmentRequest{
		Resources: &ResourcesInput{CPU: "4", Memory: "8Gi", Disk: "30Gi"},
	}, 200, &env)
	if env.Resources.CPU != "4" {
		t.Errorf("updated = %+v", env.Resources)
	}

	// Image in use can not be deleted.
	h.do("DELETE", "/api/v1/images/base", nil, 409, nil)

	h.do("DELETE", "/api/v1/environments/work", nil, 202, nil)
	h.do("GET", "/api/v1/environments/work", nil, 404, nil)
}

func TestEnvironmentValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		req  CreateEnvironmentRequest
		code int
		msg  string
	}{
		{"bad name", CreateEnvironmentRequest{Name: "Bad_Name", Image: "base"}, 400, "invalid name"},
		{"long name", CreateEnvironmentRequest{Name: strings.Repeat("a", 41), Image: "base"}, 400, "at most"},
		{"missing image", CreateEnvironmentRequest{Name: "a", Image: "nope"}, 400, "not found"},
		{"disabled image", CreateEnvironmentRequest{Name: "a", Image: "old"}, 400, "disabled"},
		{"missing class", CreateEnvironmentRequest{Name: "a", Image: "base", Class: "gpu"}, 400, "class"},
		{"bad quantity", CreateEnvironmentRequest{Name: "a", Image: "base", Resources: ResourcesInput{CPU: "lots"}}, 400, "invalid cpu"},
		{"class limit", CreateEnvironmentRequest{Name: "a", Image: "base", Resources: ResourcesInput{CPU: "7"}}, 400, "exceeds class limit"},
		{"disk limit", CreateEnvironmentRequest{Name: "a", Image: "base", Resources: ResourcesInput{Disk: "1Ti"}}, 400, "exceeds class limit"},
		{"capacity", CreateEnvironmentRequest{Name: "a", Image: "base", Resources: ResourcesInput{Memory: "32Gi"}}, 422, "insufficient capacity"},
		{"bad key", CreateEnvironmentRequest{Name: "a", Image: "base", SSHKeys: &[]string{"nope"}}, 400, "invalid public key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.req)
			rec := h.raw("POST", "/api/v1/environments", string(b), true)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("got %d %s, want %d containing %q", rec.Code, rec.Body, tc.code, tc.msg)
			}
		})
	}

	// Stopped environments skip the capacity check.
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{
		Name: "big", Image: "base", Resources: ResourcesInput{Memory: "32Gi"}, Running: new(bool),
	}, 201, nil)
	rec := h.raw("POST", "/api/v1/environments/big/start", "", true)
	if rec.Code != 422 {
		t.Errorf("starting oversized env: %d %s", rec.Code, rec.Body)
	}
}

func TestCapacityExcludesOwnPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "work"},
		Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("6"), corev1.ResourceMemory: resource.MustParse("12Gi")},
		}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	env := &devenvv1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "work"},
		Spec:       devenvv1.DevEnvironmentSpec{ImageRef: "base", Resources: devenvv1.Resources{CPU: qp("6"), Memory: qp("12Gi")}},
	}
	h := newHarness(t, pod, env)

	var c Capacity
	h.do("GET", "/api/v1/classes/default/capacity", nil, 200, &c)
	if c.MaxCPU != "2" || c.MaxDisk != "100Gi" {
		t.Errorf("capacity = %+v", c)
	}
	h.do("GET", "/api/v1/classes/default/capacity?exclude=work", nil, 200, &c)
	if c.MaxCPU != "6" { // capped by class limit
		t.Errorf("capacity with exclude = %+v", c)
	}

	// Resizing the running environment counts its own pod as free.
	h.do("PATCH", "/api/v1/environments/work", UpdateEnvironmentRequest{
		Resources: &ResourcesInput{CPU: "5", Memory: "14Gi"},
	}, 200, nil)
}

func TestImageCRUD(t *testing.T) {
	h := newHarness(t)
	var img Image
	h.do("POST", "/api/v1/images", ImageInput{
		Name: "desk", Image: "example.com/desk:1", Desktop: true, Shared: true,
		DefaultResources: ResourcesInput{CPU: "4"},
	}, 201, &img)
	if !img.Desktop || img.DefaultResources.CPU != "4" {
		t.Errorf("image = %+v", img)
	}
	h.do("POST", "/api/v1/images", ImageInput{Name: "x", Image: ""}, 400, nil)
	h.do("POST", "/api/v1/images", ImageInput{Name: "UPPER", Image: "a"}, 400, nil)

	h.do("PUT", "/api/v1/images/desk", ImageInput{Image: "example.com/desk:2", Disabled: true}, 200, &img)
	if img.Image != "example.com/desk:2" || !img.Disabled || img.Desktop {
		t.Errorf("updated = %+v", img)
	}
	var list []Image
	h.do("GET", "/api/v1/images", nil, 200, &list)
	if len(list) != 3 {
		t.Errorf("images = %d", len(list))
	}
	h.do("DELETE", "/api/v1/images/desk", nil, 204, nil)

	var classes []Class
	h.do("GET", "/api/v1/classes", nil, 200, &classes)
	if len(classes) != 1 || !classes[0].Default || classes[0].NetworkMode != "gateway" || classes[0].StorageMode != "persistent" {
		t.Errorf("classes = %+v", classes)
	}
}

func TestSSHKeys(t *testing.T) {
	h := newHarness(t)
	var k SSHKey
	h.do("POST", "/api/v1/sshkeys", map[string]string{"name": "laptop", "publicKey": testKey}, 201, &k)
	h.do("POST", "/api/v1/sshkeys", map[string]string{"publicKey": "garbage"}, 400, nil)
	var keys []SSHKey
	h.do("GET", "/api/v1/sshkeys", nil, 200, &keys)
	if len(keys) != 1 || keys[0].Name != "laptop" {
		t.Errorf("keys = %+v", keys)
	}
	h.do("DELETE", "/api/v1/sshkeys/"+k.ID, nil, 204, nil)
	h.do("DELETE", "/api/v1/sshkeys/"+k.ID, nil, 404, nil)
}

func TestSPAFallback(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/", "/environments/work"} {
		rec := h.raw("GET", p, "", false)
		if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte("spa")) {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
	rec := h.raw("GET", "/assets/app.js", "", false)
	if rec.Body.String() != "js" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %q %v", rec.Body, rec.Header())
	}
	if rec := h.raw("GET", "/api/v1/unknown", "", true); rec.Code != 404 {
		t.Errorf("unknown api route: %d", rec.Code)
	}
}

func TestPorts(t *testing.T) {
	h := newHarness(t)
	var env Environment
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{
		Name: "web", Image: "base",
		Ports: []PortInput{{Port: 3000}, {Port: 3000}, {Port: 53, Protocol: "UDP", Name: "dns"}},
	}, 201, &env)
	if len(env.Ports) != 2 || env.Ports[0].Protocol != "TCP" || env.Ports[1].Name != "dns" {
		t.Errorf("ports = %+v", env.Ports)
	}
	h.do("PATCH", "/api/v1/environments/web", UpdateEnvironmentRequest{Ports: &[]PortInput{{Port: 70000}}}, 400, nil)
	h.do("PATCH", "/api/v1/environments/web", UpdateEnvironmentRequest{Ports: &[]PortInput{{Port: 80, Protocol: "SCTP"}}}, 400, nil)
	h.do("PATCH", "/api/v1/environments/web", UpdateEnvironmentRequest{Ports: &[]PortInput{}}, 200, &env)
	if len(env.Ports) != 0 {
		t.Errorf("ports not cleared: %+v", env.Ports)
	}
}

func TestPassword(t *testing.T) {
	h := newHarness(t)
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "pw", Image: "base", Password: "short"}, 400, nil)

	var env Environment
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "pw", Image: "base", Password: "long enough pw"}, 201, &env)
	if !env.PasswordSet {
		t.Errorf("passwordSet false")
	}
	sec := &corev1.Secret{}
	if err := h.c.Get(t.Context(), client.ObjectKey{Namespace: "devenv", Name: "pw-password"}, sec); err != nil {
		t.Fatal(err)
	}
	if _, ok := sec.Data["password"]; ok {
		t.Error("plain password must not be stored")
	}
	if bcrypt.CompareHashAndPassword(sec.Data["passwordHash"], []byte("long enough pw")) != nil {
		t.Error("hash does not match")
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != "pw" {
		t.Errorf("owner refs = %+v", sec.OwnerReferences)
	}

	// Password is never returned.
	rec := h.raw("GET", "/api/v1/environments/pw", "", true)
	if strings.Contains(rec.Body.String(), "long enough") || strings.Contains(rec.Body.String(), "$2a$") {
		t.Errorf("password leaked: %s", rec.Body)
	}

	empty := ""
	h.do("PATCH", "/api/v1/environments/pw", UpdateEnvironmentRequest{Password: &empty}, 200, &env)
	if env.PasswordSet {
		t.Error("password not removed")
	}
	if err := h.c.Get(t.Context(), client.ObjectKey{Namespace: "devenv", Name: "pw-password"}, &corev1.Secret{}); err == nil {
		t.Error("password secret not deleted")
	}
	pw := "another password"
	h.do("PATCH", "/api/v1/environments/pw", UpdateEnvironmentRequest{Password: &pw}, 200, &env)
	if !env.PasswordSet {
		t.Error("password not set again")
	}
}

func TestMultiUser(t *testing.T) {
	h := newHarness(t)
	alice := h.as("alice", devenvv1.RoleUser, "dev")
	bob := h.as("bob", devenvv1.RoleUser)
	carol := h.as("carol", devenvv1.RoleUser, "ops")

	var env Environment
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a1", Image: "base", Running: new(bool)}, 201, &env)
	if env.Owner != "alice" {
		t.Fatalf("owner = %q", env.Owner)
	}
	// Bob can not see or touch it.
	bob.do("GET", "/api/v1/environments/a1", nil, 404, nil)
	bob.do("POST", "/api/v1/environments/a1/start", nil, 404, nil)
	bob.do("DELETE", "/api/v1/environments/a1", nil, 404, nil)
	var list []Environment
	bob.do("GET", "/api/v1/environments", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("bob sees %v", list)
	}
	// Only admins create for others.
	bob.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "b1", Image: "base", Owner: "alice"}, 403, nil)

	// Share with bob and the ops group.
	alice.do("PATCH", "/api/v1/environments/a1", UpdateEnvironmentRequest{Sharing: &AccessInput{Users: []string{"bob"}, Groups: []string{"ops"}}}, 200, &env)
	bob.do("GET", "/api/v1/environments/a1", nil, 200, &env)
	if env.Access != "use" {
		t.Fatalf("bob access %q", env.Access)
	}
	carol.do("POST", "/api/v1/environments/a1/start", nil, 200, nil)
	carol.do("POST", "/api/v1/environments/a1/stop", nil, 200, nil)
	// Shared users can not change or delete.
	bob.do("PATCH", "/api/v1/environments/a1", UpdateEnvironmentRequest{Resources: &ResourcesInput{CPU: "2"}}, 403, nil)
	bob.do("DELETE", "/api/v1/environments/a1", nil, 403, nil)
	bob.do("PATCH", "/api/v1/environments/a1", UpdateEnvironmentRequest{Sharing: &AccessInput{}}, 403, nil)

	// Admin sees own by default, everything with ?all=1, and can transfer.
	h.do("GET", "/api/v1/environments", nil, 200, &list)
	if len(list) != 0 {
		t.Fatalf("admin default list %v", list)
	}
	h.do("GET", "/api/v1/environments?all=1", nil, 200, &list)
	if len(list) != 1 {
		t.Fatalf("admin all list %v", list)
	}
	bobName := "bob"
	alice.do("PATCH", "/api/v1/environments/a1", UpdateEnvironmentRequest{Owner: &bobName}, 403, nil)
	h.do("PATCH", "/api/v1/environments/a1", UpdateEnvironmentRequest{Owner: &bobName}, 200, &env)
	if env.Owner != "bob" {
		t.Fatalf("transfer: %+v", env)
	}

	// Personal images.
	var img Image
	alice.do("POST", "/api/v1/images", ImageInput{Name: "alice-img", Image: "example.com/a:1", Prepull: true}, 201, &img)
	if img.Owner != "alice" || img.Prepull || !img.CanManage {
		t.Fatalf("personal image %+v", img)
	}
	alice.do("POST", "/api/v1/images", ImageInput{Name: "shared", Image: "example.com/s:1", Shared: true}, 403, nil)
	var imgs []Image
	bob.do("GET", "/api/v1/images", nil, 200, &imgs)
	for _, i := range imgs {
		if i.Name == "alice-img" {
			t.Fatal("bob sees alice's image")
		}
	}
	bob.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "b2", Image: "alice-img"}, 400, nil)
	bob.do("PUT", "/api/v1/images/base", ImageInput{Image: "x"}, 403, nil)
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a2", Image: "alice-img", Running: new(bool)}, 201, nil)

	// Class access.
	lan := &devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "lan"}, Spec: devenvv1.DevEnvironmentClassSpec{Access: devenvv1.Access{Groups: []string{"dev"}}}}
	if err := h.c.Create(t.Context(), lan); err != nil {
		t.Fatal(err)
	}
	var classes []Class
	bob.do("GET", "/api/v1/classes", nil, 200, &classes)
	if len(classes) != 1 {
		t.Fatalf("bob classes %v", classes)
	}
	bob.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "b3", Image: "base", Class: "lan", Running: new(bool)}, 400, nil)
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a3", Image: "base", Class: "lan", Running: new(bool)}, 201, nil)
	bob.do("PUT", "/api/v1/classes/lan/access", AccessInput{}, 403, nil)
	h.do("PUT", "/api/v1/classes/lan/access", AccessInput{Users: []string{"bob"}}, 200, nil)
	bob.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "b3", Image: "base", Class: "lan", Running: new(bool)}, 201, nil)

	// Admin endpoints.
	bob.do("GET", "/api/v1/admin/system", nil, 403, nil)
	var sys System
	h.do("GET", "/api/v1/admin/system", nil, 200, &sys)
	if sys.Counts["users"] != 4 {
		t.Fatalf("system %+v", sys.Counts)
	}
	if strings.Contains(h.raw("GET", "/api/v1/admin/system", "", true).Body.String(), "\"pw\"") {
		t.Fatal("admin password leaked in system config")
	}
}

func TestUserLimits(t *testing.T) {
	h := newHarness(t)
	h.srv.Config.Users.DefaultLimits = config.Limits{MaxEnvironments: 2, MaxRunning: 1, CPU: "3", Disk: "30Gi"}
	alice := h.as("alice", devenvv1.RoleUser)
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a1", Image: "base"}, 201, nil) // 2 CPU, 10Gi
	rec := alice.raw("POST", "/api/v1/environments", `{"name":"a2","image":"base"}`, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "at most 1 environments at once") {
		t.Fatalf("running limit: %d %s", rec.Code, rec.Body)
	}
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a2", Image: "base", Running: new(bool)}, 201, nil)
	rec = alice.raw("POST", "/api/v1/environments", `{"name":"a3","image":"base","running":false}`, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "at most 2 environments") {
		t.Fatalf("count limit: %d %s", rec.Code, rec.Body)
	}
	rec = alice.raw("PATCH", "/api/v1/environments/a1", `{"resources":{"cpu":"4"}}`, true)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "CPU limit") {
		t.Fatalf("cpu limit: %d %s", rec.Code, rec.Body)
	}
	var u Usage
	alice.do("GET", "/api/v1/me/usage", nil, 200, &u)
	if u.Environments != 2 || u.Running != 1 || u.CPU != "2" || u.Disk != "20Gi" || *u.Limits.MaxEnvironments != 2 {
		t.Fatalf("usage %+v", u)
	}
	// Per-user override by an admin.
	h.do("PATCH", "/api/v1/admin/users/alice", UpdateUserRequest{Limits: &LimitsInput{MaxEnvironments: new(int32(5))}}, 200, nil)
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "a3", Image: "base", Running: new(bool)}, 201, nil)
	// Admins are unlimited.
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "x1", Image: "base"}, 201, nil)
	h.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "x2", Image: "base", Resources: ResourcesInput{CPU: "1", Memory: "1Gi"}}, 201, nil)
}

func TestTokens(t *testing.T) {
	h := newHarness(t)
	alice := h.as("alice", devenvv1.RoleUser)
	var created struct {
		Token tokens.Token `json:"token"`
		Value string       `json:"value"`
	}
	alice.do("POST", "/api/v1/tokens", CreateTokenRequest{Name: "agent", Scopes: []string{"api", "mcp"}, ExpiresInDays: 30}, 201, &created)
	if !strings.HasPrefix(created.Value, "dvn_") || created.Token.ExpiresAt == nil {
		t.Fatalf("token %+v", created)
	}
	// Bearer API access acts as alice.
	req := httptest.NewRequest("GET", "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+created.Value)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"alice"`) || !strings.Contains(rec.Body.String(), `"method":"token"`) {
		t.Fatalf("bearer me: %d %s", rec.Code, rec.Body)
	}
	// Bearer requests do not need the CSRF header, but tokens can not mint tokens.
	req = httptest.NewRequest("POST", "/api/v1/tokens", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+created.Value)
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("token minting token: %d %s", rec.Code, rec.Body)
	}
	// MCP-only tokens are rejected on the API.
	var mcpOnly struct {
		Value string `json:"value"`
	}
	alice.do("POST", "/api/v1/tokens", CreateTokenRequest{Name: "mcp"}, 201, &mcpOnly)
	if _, err := h.srv.Token(t.Context(), mcpOnly.Value, "api"); err == nil {
		t.Fatal("mcp token accepted for api")
	}
	if p, err := h.srv.Token(t.Context(), mcpOnly.Value, "mcp"); err != nil || p.Name != "alice" || p.TokenName != "mcp" {
		t.Fatalf("mcp token: %+v %v", p, err)
	}
	// Legacy MCP token acts as the local admin.
	if p, err := h.srv.Token(t.Context(), "legacy-token", "mcp"); err != nil || p.Name != "admin" || !p.Admin {
		t.Fatalf("legacy: %+v %v", p, err)
	}
	if _, err := h.srv.Token(t.Context(), "legacy-token", "api"); err == nil {
		t.Fatal("legacy token accepted for api")
	}
	// Disabled users are rejected everywhere.
	h.do("PATCH", "/api/v1/admin/users/alice", UpdateUserRequest{Disabled: new(true)}, 200, nil)
	if _, err := h.srv.Token(t.Context(), mcpOnly.Value, "mcp"); err == nil {
		t.Fatal("disabled user's token accepted")
	}
	alice.do("GET", "/api/v1/me", nil, 401, nil)
	// Revoke.
	var list []tokens.Token
	h.do("GET", "/api/v1/admin/tokens", nil, 200, &list)
	if len(list) != 2 {
		t.Fatalf("tokens %v", list)
	}
	h.do("DELETE", "/api/v1/admin/tokens/"+list[0].ID, nil, 204, nil)
}

func TestMigrate(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "devenv-user-abc",
			Labels:      map[string]string{"devenv.tinyrack.net/component": "user"},
			Annotations: map[string]string{"devenv.tinyrack.net/user": "admin"}},
		Data: map[string]string{"keys.json": `[{"name":"laptop","publicKey":"` + testKey + `","createdAt":"2026-01-01T00:00:00Z"}]`},
	}
	mcpEnv := &devenvv1.DevEnvironment{ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "agent1"}, Spec: devenvv1.DevEnvironmentSpec{Owner: "mcp", ImageRef: "base"}}
	adminEnv := &devenvv1.DevEnvironment{ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "mine"}, Spec: devenvv1.DevEnvironmentSpec{Owner: "admin", ImageRef: "base"}}
	h := newHarness(t, cm, mcpEnv, adminEnv)
	for range 2 { // idempotent
		if err := h.srv.Users.Migrate(t.Context(), h.c, "devenv"); err != nil {
			t.Fatal(err)
		}
	}
	u := &devenvv1.DevUser{}
	if err := h.c.Get(t.Context(), client.ObjectKey{Name: "admin"}, u); err != nil || !u.IsAdmin() || len(u.Spec.SSHKeys) != 1 {
		t.Fatalf("admin user %+v %v", u.Spec, err)
	}
	if err := h.c.Get(t.Context(), client.ObjectKeyFromObject(cm), &corev1.ConfigMap{}); err == nil {
		t.Fatal("ConfigMap not removed")
	}
	e := &devenvv1.DevEnvironment{}
	_ = h.c.Get(t.Context(), client.ObjectKeyFromObject(mcpEnv), e)
	if e.Spec.Owner != "admin" || e.Labels[devenvv1.LabelOwner] != "admin" {
		t.Fatalf("mcp env %+v %v", e.Spec.Owner, e.Labels)
	}
	var keys []SSHKey
	h.do("GET", "/api/v1/sshkeys", nil, 200, &keys)
	if len(keys) != 1 || keys[0].Name != "laptop" {
		t.Fatalf("keys %+v", keys)
	}
}

func TestChangeClass(t *testing.T) {
	sc := "longhorn"
	other := "fast"
	lan := &devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "lan"}, Spec: devenvv1.DevEnvironmentClassSpec{
		Network: devenvv1.NetworkSpec{Mode: devenvv1.NetworkModeLoadBalancer}, Access: devenvv1.Access{Groups: []string{"lan-users"}}}}
	def := &devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Spec: devenvv1.DevEnvironmentClassSpec{
		Storage: devenvv1.StorageSpec{StorageClassName: &sc}, Limits: devenvv1.Limits{MaxCPU: qp("6")}}}
	lan.Spec.Storage.StorageClassName = &sc
	fast := &devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Spec: devenvv1.DevEnvironmentClassSpec{Storage: devenvv1.StorageSpec{StorageClassName: &other}}}
	eph := &devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "eph"}, Spec: devenvv1.DevEnvironmentClassSpec{Storage: devenvv1.StorageSpec{Mode: devenvv1.StorageModeEphemeral}}}
	h := newHarness(t, lan, fast, eph)
	// The harness default class has no storage class; replace it.
	cur := &devenvv1.DevEnvironmentClass{}
	_ = h.c.Get(t.Context(), client.ObjectKey{Name: "default"}, cur)
	cur.Spec = def.Spec
	_ = h.c.Update(t.Context(), cur)

	alice := h.as("alice", devenvv1.RoleUser)
	alice.do("POST", "/api/v1/environments", CreateEnvironmentRequest{Name: "w", Image: "base", Running: new(bool)}, 201, nil)
	for _, tc := range []struct {
		class, msg string
		code       int
	}{{"nope", "not found", 400}, {"lan", "not found", 400}, {"fast", "storage class differs", 400}, {"eph", "storage mode differs", 400}} {
		rec := alice.raw("PATCH", "/api/v1/environments/w", `{"class":"`+tc.class+`"}`, true)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.msg) {
			t.Errorf("%s: %d %s", tc.class, rec.Code, rec.Body)
		}
	}
	// With access to lan the move works.
	alice = h.as("alice", devenvv1.RoleUser, "lan-users")
	var env Environment
	alice.do("PATCH", "/api/v1/environments/w", UpdateEnvironmentRequest{Class: new("lan")}, 200, &env)
	if env.Class != "lan" {
		t.Fatalf("class = %s", env.Class)
	}
	// Shared users can not move it.
	alice.do("PATCH", "/api/v1/environments/w", UpdateEnvironmentRequest{Sharing: &AccessInput{Users: []string{"bob"}}}, 200, nil)
	h.as("bob", devenvv1.RoleUser, "lan-users").do("PATCH", "/api/v1/environments/w", UpdateEnvironmentRequest{Class: new("default")}, 403, nil)
}
