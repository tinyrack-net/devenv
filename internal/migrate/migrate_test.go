package migrate

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/store/storetest"
)

func obj(kind, ns, name string, labels map[string]string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range fields {
		u.Object[k] = v
	}
	u.SetAPIVersion("devenv.tinyrack.net/v1alpha1")
	u.SetKind(kind)
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetLabels(labels)
	return u
}

// TestRun imports the state of 0.10 (CRDs and Secrets in a real API server)
// and releases the objects from the CRD owners.
func TestRun(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("testdata", "crds")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer env.Stop()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, ns := range []string{"devenv", "devenv-system"} {
		must(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	}
	hostPEM, _ := keys.NewEd25519PEM()
	clientPEM, _ := keys.NewEd25519PEM()
	must(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "devenv-keys", Namespace: "devenv-system"}, Data: map[string][]byte{
		"session-key": []byte("0123456789abcdef0123456789abcdef"), "gateway-host-key": []byte(hostPEM), "client-key": []byte(clientPEM), "mcp-token": []byte("dvm_legacy"),
	}}))
	must(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "devenv-token-abcdefgh", Namespace: "devenv-system",
		Labels:      map[string]string{"devenv.tinyrack.net/component": "token", "devenv.tinyrack.net/user": "alice"},
		Annotations: map[string]string{"devenv.tinyrack.net/name": "agent", "devenv.tinyrack.net/user": "alice", "devenv.tinyrack.net/scopes": "mcp,api"}},
		Data: map[string][]byte{"hash": []byte("h")}}))

	must(c.Create(ctx, obj("DevUser", "", "alice", nil, map[string]any{"spec": map[string]any{"email": "a@example.com", "role": "admin",
		"sshKeys": []any{map[string]any{"name": "k", "publicKey": "ssh-ed25519 AAAA"}}}})))
	must(c.Create(ctx, obj("DevImage", "", "base", map[string]string{"app.kubernetes.io/managed-by": "Helm"}, map[string]any{"spec": map[string]any{"image": "x:1"}})))
	must(c.Create(ctx, obj("DevImage", "", "mine", nil, map[string]any{"spec": map[string]any{"image": "y:1", "owner": "alice"}})))
	e := obj("DevEnvironment", "devenv", "work", map[string]string{"devenv.tinyrack.net/created-by": "mcp"}, map[string]any{"spec": map[string]any{
		"owner": "alice", "imageRef": "base", "className": "lan", "running": true, "passwordSecretName": "work-password",
		"resources": map[string]any{"cpu": "2"}}})
	must(c.Create(ctx, e))
	e.Object["status"] = map[string]any{"phase": "Running", "networkMode": "multus", "mac": "02:11:22:33:44:55"}
	must(c.Status().Update(ctx, e))

	owner := []metav1.OwnerReference{{APIVersion: "devenv.tinyrack.net/v1alpha1", Kind: "DevEnvironment", Name: "work", UID: e.GetUID(), Controller: new(true)}}
	must(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "work-ssh", Namespace: "devenv", OwnerReferences: owner},
		Data: map[string][]byte{"ssh_host_ed25519_key": []byte("PRIV"), "ssh_host_ed25519_key.pub": []byte("PUB"), "password_hash": []byte("")}}))
	must(c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "work-password", Namespace: "devenv", OwnerReferences: owner},
		Data: map[string][]byte{"passwordHash": []byte("$2a$10$abcdefghijklmnopqrstuuJ4aJ2V8dQn4bF6j4bq3Gx2K8tU1l3e")}}))
	must(c.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "work-home", Namespace: "devenv", OwnerReferences: owner},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}}}))

	db := storetest.New(t)
	opts := Options{EnvironmentNamespace: "devenv", ServerNamespace: "devenv-system", KeysSecret: "devenv-keys", Log: logr.Discard()}
	res, err := Run(ctx, c, db, opts)
	must(err)
	if res.Users != 1 || res.Images != 1 || res.Environments != 1 || res.Tokens != 1 || !res.Keys {
		t.Fatalf("result %+v", res)
	}
	if res, _ := Run(ctx, c, db, opts); !res.Skipped {
		t.Fatal("second run imported again")
	}

	k, err := keys.Ensure(ctx, db)
	must(err)
	if string(k.SessionKey) != "0123456789abcdef0123456789abcdef" || k.MCPToken != "dvm_legacy" {
		t.Fatalf("keys not imported: %q %q", k.SessionKey, k.MCPToken)
	}
	if v, _ := db.GetValue(ctx, keys.KeySession); v != base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")) {
		t.Fatalf("session key %q", v)
	}
	u, err := db.GetUser(ctx, "alice")
	must(err)
	if u.Spec.Email != "a@example.com" || len(u.Spec.SSHKeys) != 1 || !u.IsAdmin() {
		t.Fatalf("user %+v", u)
	}
	if _, err := db.GetImage(ctx, "base"); err == nil {
		t.Fatal("helm image imported")
	}
	w, err := db.GetEnvironment(ctx, "work")
	must(err)
	if w.Spec.Owner != "alice" || w.Spec.ClassName != "lan" || w.Spec.Resources.CPU.String() != "2" || w.CreatedBy != "mcp" ||
		string(w.HostKey) != "PRIV" || string(w.HostKeyPub) != "PUB" || w.PasswordHash == "" || !w.VolumeCreated ||
		w.Status.StableMAC != "02:11:22:33:44:55" {
		t.Fatalf("environment %+v", w)
	}
	tok, err := db.GetToken(ctx, "abcdefgh")
	must(err)
	if tok.User != "alice" || len(tok.Scopes) != 2 || tok.Hash != "h" {
		t.Fatalf("token %+v", tok)
	}
	for _, n := range []struct {
		o    client.Object
		name string
	}{{&corev1.Secret{}, "work-ssh"}, {&corev1.Secret{}, "work-password"}, {&corev1.PersistentVolumeClaim{}, "work-home"}} {
		must(c.Get(ctx, client.ObjectKey{Namespace: "devenv", Name: n.name}, n.o))
		if len(n.o.GetOwnerReferences()) != 0 {
			t.Errorf("%s still owned: %v", n.name, n.o.GetOwnerReferences())
		}
	}
}

func TestRunWithoutCRDs(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
	env := &envtest.Environment{}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer env.Stop()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	c, _ := client.New(cfg, client.Options{Scheme: s})
	db := storetest.New(t)
	res, err := Run(context.Background(), c, db, Options{EnvironmentNamespace: "devenv", ServerNamespace: "devenv-system", KeysSecret: "devenv-keys", Log: logr.Discard()})
	if err != nil || res.Environments != 0 || res.Keys {
		t.Fatalf("fresh install: %+v %v", res, err)
	}
}
