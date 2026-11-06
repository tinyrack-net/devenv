package controller

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

func qty(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// fixture creates a namespace, image and class unique to the test.
type fixture struct {
	ns    string
	image string
	class string
}

func newFixture(t *testing.T, classSpec devenvv1.DevEnvironmentClassSpec, desktop bool) fixture {
	t.Helper()
	name := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()))
	f := fixture{ns: name, image: name + "-img", class: name + "-class"}

	must(t, k8sClient.Create(testCtx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.ns}}))
	must(t, k8sClient.Create(testCtx, &devenvv1.DevImage{
		ObjectMeta: metav1.ObjectMeta{Name: f.image},
		Spec: devenvv1.DevImageSpec{
			Image:   "ghcr.io/tinyrack-net/devenv-base:latest",
			Desktop: desktop,
			DefaultResources: devenvv1.Resources{
				CPU: qty("2"), Memory: qty("4Gi"),
			},
		},
	}))
	must(t, k8sClient.Create(testCtx, &devenvv1.DevEnvironmentClass{
		ObjectMeta: metav1.ObjectMeta{Name: f.class},
		Spec:       classSpec,
	}))
	return f
}

func (f fixture) env(name string) *devenvv1.DevEnvironment {
	return &devenvv1.DevEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: devenvv1.DevEnvironmentSpec{
			ImageRef:          f.image,
			ClassName:         f.class,
			SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA test@example"},
		},
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(obj client.Object, ns, name string) error {
	return k8sClient.Get(testCtx, types.NamespacedName{Namespace: ns, Name: name}, obj)
}

func waitPhase(t *testing.T, env *devenvv1.DevEnvironment, phase devenvv1.Phase) *devenvv1.DevEnvironment {
	t.Helper()
	cur := &devenvv1.DevEnvironment{}
	eventually(t, func() error {
		if err := get(cur, env.Namespace, env.Name); err != nil {
			return err
		}
		if cur.Status.Phase != phase {
			return fmt.Errorf("phase %q (%s), want %q", cur.Status.Phase, cur.Status.Message, phase)
		}
		return nil
	})
	return cur
}

func isOwnedBy(obj metav1.Object, env *devenvv1.DevEnvironment) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == env.UID && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func TestCreatesChildren(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{}, true)
	env := f.env("dev1")
	must(t, k8sClient.Create(testCtx, env))

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, f.ns, "dev1") })

	if !isOwnedBy(pod, env) {
		t.Errorf("pod not owned by environment")
	}
	c := pod.Spec.Containers[0]
	if got := c.Resources.Limits.Cpu().String(); got != "2" {
		t.Errorf("cpu limit = %s, want image default 2", got)
	}
	if got := c.Resources.Requests.Memory().String(); got != "4Gi" {
		t.Errorf("memory request = %s, want 4Gi", got)
	}
	if pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "dev1-home" {
		t.Errorf("home volume not bound to pvc: %+v", pod.Spec.Volumes[0])
	}
	if pod.Annotations[AnnotationSpecHash] == "" {
		t.Errorf("missing spec hash annotation")
	}

	pvc := &corev1.PersistentVolumeClaim{}
	must(t, get(pvc, f.ns, "dev1-home"))
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != devenvv1.DefaultDisk {
		t.Errorf("pvc size = %s, want %s", got, devenvv1.DefaultDisk)
	}
	if !isOwnedBy(pvc, env) {
		t.Errorf("pvc not owned by environment")
	}

	secret := &corev1.Secret{}
	must(t, get(secret, f.ns, "dev1-ssh"))
	if got := string(secret.Data["authorized_keys"]); got != "ssh-ed25519 AAAA test@example\n" {
		t.Errorf("authorized_keys = %q", got)
	}
	hostKey := string(secret.Data[HostKeyFile])
	if !strings.Contains(hostKey, "OPENSSH PRIVATE KEY") || !strings.HasPrefix(string(secret.Data[HostPublicKeyFile]), "ssh-ed25519 ") {
		t.Errorf("host key not generated: %v", secret.Data)
	}

	// Updating keys keeps the host key.
	must(t, k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env))
	env.Spec.SSHAuthorizedKeys = []string{"ssh-ed25519 BBBB other@example"}
	must(t, k8sClient.Update(testCtx, env))
	eventually(t, func() error {
		s := &corev1.Secret{}
		if err := get(s, f.ns, "dev1-ssh"); err != nil {
			return err
		}
		if !strings.Contains(string(s.Data["authorized_keys"]), "BBBB") {
			return fmt.Errorf("keys not updated")
		}
		if string(s.Data[HostKeyFile]) != hostKey {
			return fmt.Errorf("host key changed")
		}
		return nil
	})

	svc := &corev1.Service{}
	must(t, get(svc, f.ns, "dev1"))
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != SSHPort {
		t.Errorf("only ssh should be exposed (vnc is tunneled), got %+v", svc.Spec.Ports)
	}

	cur := waitPhase(t, env, devenvv1.PhaseStarting)
	if cur.Status.Image != "ghcr.io/tinyrack-net/devenv-base:latest" || !cur.Status.Desktop {
		t.Errorf("status image/desktop not populated: %+v", cur.Status)
	}
}

func TestEphemeralStorage(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Storage: devenvv1.StorageSpec{Mode: devenvv1.StorageModeEphemeral},
	}, false)
	must(t, k8sClient.Create(testCtx, f.env("dev1")))

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, f.ns, "dev1") })
	if pod.Spec.Volumes[0].EmptyDir == nil {
		t.Errorf("expected emptyDir home volume")
	}
	err := get(&corev1.PersistentVolumeClaim{}, f.ns, "dev1-home")
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected no pvc, got err=%v", err)
	}
}

func TestStopAndStart(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{}, false)
	env := f.env("dev1")
	must(t, k8sClient.Create(testCtx, env))
	eventually(t, func() error { return get(&corev1.Pod{}, f.ns, "dev1") })

	// Stop: pod goes away, pvc stays.
	must(t, k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env))
	env.Spec.Running = ptr.To(false)
	must(t, k8sClient.Update(testCtx, env))

	waitPhase(t, env, devenvv1.PhaseStopped)
	if err := get(&corev1.Pod{}, f.ns, "dev1"); !apierrors.IsNotFound(err) {
		t.Errorf("pod should be deleted, err=%v", err)
	}
	must(t, get(&corev1.PersistentVolumeClaim{}, f.ns, "dev1-home"))

	// Start again.
	must(t, k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env))
	env.Spec.Running = ptr.To(true)
	must(t, k8sClient.Update(testCtx, env))
	eventually(t, func() error { return get(&corev1.Pod{}, f.ns, "dev1") })
}

func TestSpecChangeRecreatesPod(t *testing.T) {
	scName := "expandable"
	must(t, client.IgnoreAlreadyExists(k8sClient.Create(testCtx, &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: scName},
		Provisioner:          "example.com/fake",
		AllowVolumeExpansion: ptr.To(true),
	})))
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Storage: devenvv1.StorageSpec{StorageClassName: &scName},
	}, false)
	env := f.env("dev1")
	must(t, k8sClient.Create(testCtx, env))

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, f.ns, "dev1") })
	oldUID := pod.UID

	// envtest has no provisioner; mark the claim bound so resizing is allowed.
	pvc := &corev1.PersistentVolumeClaim{}
	must(t, get(pvc, f.ns, "dev1-home"))
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = pvc.Spec.Resources.Requests
	must(t, k8sClient.Status().Update(testCtx, pvc))

	must(t, k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env))
	env.Spec.Resources.CPU = qty("3")
	env.Spec.Resources.Disk = qty("20Gi")
	must(t, k8sClient.Update(testCtx, env))

	eventually(t, func() error {
		p := &corev1.Pod{}
		if err := get(p, f.ns, "dev1"); err != nil {
			return err
		}
		if p.UID == oldUID {
			return fmt.Errorf("pod not recreated yet")
		}
		if got := p.Spec.Containers[0].Resources.Limits.Cpu().String(); got != "3" {
			return fmt.Errorf("cpu = %s", got)
		}
		return nil
	})
	eventually(t, func() error {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := get(pvc, f.ns, "dev1-home"); err != nil {
			return err
		}
		if got := pvc.Spec.Resources.Requests.Storage().String(); got != "20Gi" {
			return fmt.Errorf("pvc size = %s", got)
		}
		return nil
	})
}

func TestClassLimits(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Limits: devenvv1.Limits{MaxCPU: qty("1")},
	}, false)
	env := f.env("dev1") // image default cpu 2 > max 1
	must(t, k8sClient.Create(testCtx, env))

	cur := waitPhase(t, env, devenvv1.PhaseFailed)
	if !strings.Contains(cur.Status.Message, "exceeds class limit") {
		t.Errorf("message = %q", cur.Status.Message)
	}
	if err := get(&corev1.Pod{}, f.ns, "dev1"); !apierrors.IsNotFound(err) {
		t.Errorf("pod should not be created, err=%v", err)
	}
}

func TestMissingImageThenCreated(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{}, false)
	env := f.env("dev1")
	env.Spec.ImageRef = f.image + "-late"
	must(t, k8sClient.Create(testCtx, env))
	waitPhase(t, env, devenvv1.PhasePending)

	must(t, k8sClient.Create(testCtx, &devenvv1.DevImage{
		ObjectMeta: metav1.ObjectMeta{Name: f.image + "-late"},
		Spec:       devenvv1.DevImageSpec{Image: "example.com/late:1"},
	}))
	eventually(t, func() error { return get(&corev1.Pod{}, f.ns, "dev1") })
}

func TestPodOverrides(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		PodOverrides: &runtime.RawExtension{Raw: []byte(
			`{"metadata":{"labels":{"extra":"yes","devenv.tinyrack.net/environment":"hijack"}},"spec":{"hostUsers":false}}`)},
	}, false)
	must(t, k8sClient.Create(testCtx, f.env("dev1")))

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, f.ns, "dev1") })
	if pod.Labels["extra"] != "yes" {
		t.Errorf("override label missing")
	}
	if pod.Labels[LabelEnvironment] != "dev1" {
		t.Errorf("identity label must not be overridden, got %q", pod.Labels[LabelEnvironment])
	}
	if pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
		t.Errorf("hostUsers override not applied")
	}
}

func TestUnsupportedNetworkMode(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Network: devenvv1.NetworkSpec{Mode: devenvv1.NetworkModeLoadBalancer},
	}, false)
	env := f.env("dev1")
	must(t, k8sClient.Create(testCtx, env))
	waitPhase(t, env, devenvv1.PhaseFailed)
}

func TestCELValidation(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{}, false)
	env := f.env("dev1")
	env.Spec.Resources.Disk = qty("20Gi")
	must(t, k8sClient.Create(testCtx, env))

	t.Run("disk shrink rejected", func(t *testing.T) {
		cur := &devenvv1.DevEnvironment{}
		must(t, get(cur, f.ns, "dev1"))
		cur.Spec.Resources.Disk = qty("10Gi")
		if err := k8sClient.Update(testCtx, cur); err == nil || !strings.Contains(err.Error(), "disk can not be shrunk") {
			t.Errorf("expected shrink rejection, got %v", err)
		}
	})
	t.Run("class immutable", func(t *testing.T) {
		cur := &devenvv1.DevEnvironment{}
		must(t, get(cur, f.ns, "dev1"))
		cur.Spec.ClassName = "other"
		if err := k8sClient.Update(testCtx, cur); err == nil || !strings.Contains(err.Error(), "className is immutable") {
			t.Errorf("expected immutability error, got %v", err)
		}
	})
	t.Run("multus requires config", func(t *testing.T) {
		err := k8sClient.Create(testCtx, &devenvv1.DevEnvironmentClass{
			ObjectMeta: metav1.ObjectMeta{Name: f.class + "-multus"},
			Spec:       devenvv1.DevEnvironmentClassSpec{Network: devenvv1.NetworkSpec{Mode: devenvv1.NetworkModeMultus}},
		})
		if err == nil || !strings.Contains(err.Error(), "multus is required") {
			t.Errorf("expected multus validation error, got %v", err)
		}
	})
}
