package backup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
)

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = devenvv1.AddToScheme(s)
	return s
}

func cluster(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme()).WithObjects(objs...).
		WithStatusSubresource(&devenvv1.DevEnvironment{}, &devenvv1.DevUser{}).Build()
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	q := resource.MustParse("2")
	src := cluster(
		&devenvv1.DevUser{ObjectMeta: metav1.ObjectMeta{Name: "alice", ResourceVersion: "5", UID: "u1"}, Spec: devenvv1.DevUserSpec{Email: "a@example.com", SSHKeys: []devenvv1.UserSSHKey{{Name: "k", PublicKey: "ssh-ed25519 AAAA"}}}},
		&devenvv1.DevImage{ObjectMeta: metav1.ObjectMeta{Name: "base"}, Spec: devenvv1.DevImageSpec{Image: "example.com/base:1"}},
		&devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "lan"}, Spec: devenvv1.DevEnvironmentClassSpec{Access: devenvv1.Access{Groups: []string{"devs"}}}},
		&devenvv1.DevEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "work", Namespace: "devenv", UID: "e1"},
			Spec:       devenvv1.DevEnvironmentSpec{Owner: "alice", ImageRef: "base", ClassName: "lan", Running: ptr.To(true), Resources: devenvv1.Resources{CPU: &q}},
			Status:     devenvv1.DevEnvironmentStatus{Phase: devenvv1.PhaseRunning, StableMAC: "02:00:00:00:00:01"},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "work-ssh", Namespace: "devenv", Labels: map[string]string{"app.kubernetes.io/managed-by": "devenv", "devenv.tinyrack.net/environment": "work"}}, Data: map[string][]byte{"ssh_host_ed25519_key.pub": []byte("HOSTKEY")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "devenv-keys", Namespace: "devenv-system"}, Data: map[string][]byte{"session-key": []byte("S")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "devenv-token-abcdefgh", Namespace: "devenv-system", Labels: map[string]string{"devenv.tinyrack.net/component": "token"}}, Data: map[string][]byte{"hash": []byte("H")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "devenv-system"}},
	)
	opts := Options{EnvironmentNamespace: "devenv", ServerNamespace: "devenv-system", KeysSecret: "devenv-keys"}
	b, err := Export(ctx, src, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Users) != 1 || len(b.Environments) != 1 || len(b.ServerSecrets) != 2 || len(b.Environments[0].Secrets) != 1 {
		t.Fatalf("export %+v", b)
	}
	if b.Users[0].ResourceVersion != "" || b.Users[0].UID != "" || !b.Environments[0].WasRunning || b.Environments[0].MAC != "02:00:00:00:00:01" {
		t.Fatalf("metadata not cleaned or state missing: %+v", b.Environments[0])
	}

	// Store round trip.
	st, err := OpenStore("file://" + filepath.Join(t.TempDir(), "b"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := b.Marshal()
	for i := range 3 {
		_ = st.Put(ctx, Name(time.Unix(int64(1000+i), 0)), data)
	}
	removed, _ := Prune(ctx, st, 2)
	latest, _ := Resolve(ctx, st, Latest)
	if len(removed) != 1 || latest != Name(time.Unix(1002, 0)) {
		t.Fatalf("prune %v latest %s", removed, latest)
	}
	raw, _ := st.Get(ctx, latest)
	b2, err := Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Restore into an empty cluster that already has the class from GitOps
	// (spec differs, access must come from the backup).
	dst := cluster(&devenvv1.DevEnvironmentClass{ObjectMeta: metav1.ObjectMeta{Name: "lan"}, Spec: devenvv1.DevEnvironmentClassSpec{Network: devenvv1.NetworkSpec{Mode: devenvv1.NetworkModeMultus}}})
	res, err := Restore(ctx, dst, b2, RestoreOptions{Options: opts, Start: true, VolumeTimeout: 0})
	if err != nil {
		t.Fatal(err)
	}
	env := &devenvv1.DevEnvironment{}
	if err := dst.Get(ctx, client.ObjectKey{Namespace: "devenv", Name: "work"}, env); err != nil {
		t.Fatal(err)
	}
	if env.IsRunning() || env.Annotations[controller.AnnotationAwaitVolume] == "" || env.Annotations[controller.AnnotationStableMAC] != "02:00:00:00:00:01" {
		t.Fatalf("restored env %+v %v", env.Spec, env.Annotations)
	}
	if len(res.Waiting) != 1 || len(res.Started) != 0 {
		t.Fatalf("no volume yet, should wait: %+v", res)
	}
	cl := &devenvv1.DevEnvironmentClass{}
	_ = dst.Get(ctx, client.ObjectKey{Name: "lan"}, cl)
	if cl.Spec.Network.Mode != devenvv1.NetworkModeMultus || len(cl.Spec.Access.Groups) != 1 {
		t.Fatalf("class %+v", cl.Spec)
	}
	for _, key := range []client.ObjectKey{{Namespace: "devenv", Name: "work-ssh"}, {Namespace: "devenv-system", Name: "devenv-keys"}, {Namespace: "devenv-system", Name: "devenv-token-abcdefgh"}} {
		if err := dst.Get(ctx, key, &corev1.Secret{}); err != nil {
			t.Errorf("secret %s: %v", key, err)
		}
	}
	if err := dst.Get(ctx, client.ObjectKey{Namespace: "devenv-system", Name: "unrelated"}, &corev1.Secret{}); err == nil {
		t.Error("unrelated secret restored")
	}

	// Once the storage system restores the volume the environment starts.
	_ = dst.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "work-home", Namespace: "devenv"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"}})
	res, err = Restore(ctx, dst, b2, RestoreOptions{Options: opts, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = dst.Get(ctx, client.ObjectKey{Namespace: "devenv", Name: "work"}, env)
	if len(res.Started) != 1 || !env.IsRunning() || res.Kept == 0 {
		t.Fatalf("second restore %+v running=%v", res, env.IsRunning())
	}
}
