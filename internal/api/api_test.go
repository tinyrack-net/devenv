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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
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
		WithStatusSubresource(&devenvv1.DevEnvironment{}).Build()
	sessions, _ := auth.NewSessions([]byte("k"), false)
	srv := &Server{
		Client:    c,
		Reader:    c,
		Namespace: "devenv",
		Auth:      &auth.PasswordAuthenticator{Username: "admin", Password: "pw"},
		Sessions:  sessions,
		Capacity:  &capacity.Checker{Reader: c, Namespace: "devenv"},
		Static:    fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}, "assets/app.js": {Data: []byte("js")}},
	}
	h := &harness{t: t, srv: srv, h: srv.Handler(), c: c}
	h.login()
	return h
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
	if info.AuthMode != "password" {
		t.Errorf("info = %+v", info)
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	h := newHarness(t)

	// Stored key is used when no keys are given.
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
	if env.Class != "default" || !env.Running || env.Owner != "admin" || len(env.SSHKeys) != 1 {
		t.Errorf("created = %+v", env)
	}

	obj := &devenvv1.DevEnvironment{}
	if err := h.c.Get(t.Context(), client.ObjectKey{Namespace: "devenv", Name: "work"}, obj); err != nil {
		t.Fatal(err)
	}
	if obj.Spec.Resources.Disk.String() != "20Gi" {
		t.Errorf("disk = %v", obj.Spec.Resources.Disk)
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
		Name: "desk", Image: "example.com/desk:1", Desktop: true,
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
