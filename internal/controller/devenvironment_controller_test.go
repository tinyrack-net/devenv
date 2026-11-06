package controller

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
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
	updateEnv(t, env, func() {
		env.Spec.SSHAuthorizedKeys = []string{"ssh-ed25519 BBBB other@example"}
	})
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
	updateEnv(t, env, func() {
		env.Spec.Running = ptr.To(false)
	})

	waitPhase(t, env, devenvv1.PhaseStopped)
	if err := get(&corev1.Pod{}, f.ns, "dev1"); !apierrors.IsNotFound(err) {
		t.Errorf("pod should be deleted, err=%v", err)
	}
	must(t, get(&corev1.PersistentVolumeClaim{}, f.ns, "dev1-home"))

	// Start again.
	updateEnv(t, env, func() {
		env.Spec.Running = ptr.To(true)
	})
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

	updateEnv(t, env, func() {
		env.Spec.Resources.CPU = qty("3")
		env.Spec.Resources.Disk = qty("20Gi")
	})

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

func TestLoadBalancerMode(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Network: devenvv1.NetworkSpec{
			Mode: devenvv1.NetworkModeLoadBalancer,
			LoadBalancer: &devenvv1.LoadBalancerSpec{
				Labels:      map[string]string{"pool": "lan"},
				Annotations: map[string]string{"lb.example/ip": "auto"},
			},
		},
	}, false)
	env := f.env("dev1")
	env.Spec.Ports = []devenvv1.Port{{Port: 8080, Protocol: "TCP"}, {Port: 22, Protocol: "TCP"}, {Port: 53, Protocol: "UDP"}}
	must(t, k8sClient.Create(testCtx, env))

	svc := &corev1.Service{}
	eventually(t, func() error { return get(svc, f.ns, "dev1-lb") })
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || svc.Labels["pool"] != "lan" || svc.Annotations["lb.example/ip"] != "auto" {
		t.Errorf("service = %+v", svc.ObjectMeta)
	}
	if len(svc.Spec.Ports) != 3 {
		t.Errorf("ports = %+v (ssh, 8080/tcp, 53/udp expected)", svc.Spec.Ports)
	}

	// The load balancer assigns an address.
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}}
	must(t, k8sClient.Status().Update(testCtx, svc))
	eventually(t, func() error {
		cur := &devenvv1.DevEnvironment{}
		if err := get(cur, f.ns, "dev1"); err != nil {
			return err
		}
		if cur.Status.IP != "192.0.2.10" || cur.Status.NetworkMode != devenvv1.NetworkModeLoadBalancer {
			return fmt.Errorf("status ip=%q mode=%q", cur.Status.IP, cur.Status.NetworkMode)
		}
		return nil
	})

	// Stopping keeps the address.
	updateEnv(t, env, func() {
		env.Spec.Running = ptr.To(false)
	})
	cur := waitPhase(t, env, devenvv1.PhaseStopped)
	if cur.Status.IP != "192.0.2.10" {
		t.Errorf("ip lost on stop: %q", cur.Status.IP)
	}
	must(t, get(&corev1.Service{}, f.ns, "dev1-lb"))
}

func TestMultusMode(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{
		Network: devenvv1.NetworkSpec{
			Mode:   devenvv1.NetworkModeMultus,
			Multus: &devenvv1.MultusSpec{NetworkAttachment: "kube-system/lan", StableMAC: true},
		},
	}, false)
	env := f.env("dev1")
	must(t, k8sClient.Create(testCtx, env))

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, f.ns, "dev1") })
	var sel []multusSelection
	if err := json.Unmarshal([]byte(pod.Annotations[annotationNetworks]), &sel); err != nil || len(sel) != 1 {
		t.Fatalf("networks annotation = %q", pod.Annotations[annotationNetworks])
	}
	must(t, k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env))
	if sel[0].Namespace != "kube-system" || sel[0].Name != "lan" || sel[0].Interface != "net1" || sel[0].MAC != stableMAC(env) {
		t.Errorf("selection = %+v", sel[0])
	}
	if mac, _ := net.ParseMAC(sel[0].MAC); mac[0]&0x02 == 0 || mac[0]&0x01 != 0 {
		t.Errorf("mac %s must be locally administered unicast", sel[0].MAC)
	}

	// Multus reports the attachment.
	pod.Annotations[annotationNetworkStatus] = `[{"name":"cilium","interface":"eth0","ips":["10.0.0.5"],"default":true},` +
		`{"name":"kube-system/lan","interface":"net1","ips":["fe80::1","192.168.1.50"],"mac":"` + sel[0].MAC + `"}]`
	must(t, k8sClient.Update(testCtx, pod))
	eventually(t, func() error {
		cur := &devenvv1.DevEnvironment{}
		if err := get(cur, f.ns, "dev1"); err != nil {
			return err
		}
		if cur.Status.IP != "192.168.1.50" || cur.Status.MAC != sel[0].MAC {
			return fmt.Errorf("ip=%q mac=%q", cur.Status.IP, cur.Status.MAC)
		}
		return nil
	})
	if err := get(&corev1.Service{}, f.ns, "dev1-lb"); !apierrors.IsNotFound(err) {
		t.Errorf("no lb service expected in multus mode: %v", err)
	}
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

func TestPassword(t *testing.T) {
	f := newFixture(t, devenvv1.DevEnvironmentClassSpec{}, false)
	must(t, k8sClient.Create(testCtx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.ns, Name: "dev1-password"},
		StringData: map[string]string{PasswordKey: "first-password"},
	}))
	env := f.env("dev1")
	env.Spec.PasswordSecretName = "dev1-password"
	must(t, k8sClient.Create(testCtx, env))

	hashOf := func() (string, error) {
		s := &corev1.Secret{}
		if err := get(s, f.ns, "dev1-ssh"); err != nil {
			return "", err
		}
		return string(s.Data[PasswordHashFile]), nil
	}
	var first string
	eventually(t, func() error {
		h, err := hashOf()
		if err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(h), []byte("first-password")) != nil {
			return fmt.Errorf("hash %q does not match", h)
		}
		first = h
		return nil
	})
	cur := &devenvv1.DevEnvironment{}
	eventually(t, func() error {
		if err := get(cur, f.ns, "dev1"); err != nil {
			return err
		}
		if !cur.Status.PasswordSet {
			return fmt.Errorf("passwordSet false")
		}
		return nil
	})

	// Unrelated reconciles keep the same hash (no re-hashing).
	updateEnv(t, env, func() {
		env.Spec.SSHAuthorizedKeys = []string{"ssh-ed25519 CCCC x"}
	})
	eventually(t, func() error {
		s := &corev1.Secret{}
		if err := get(s, f.ns, "dev1-ssh"); err != nil {
			return err
		}
		if !strings.Contains(string(s.Data[AuthorizedKeysFile]), "CCCC") {
			return fmt.Errorf("keys not updated")
		}
		if string(s.Data[PasswordHashFile]) != first {
			return fmt.Errorf("hash changed without password change")
		}
		return nil
	})

	// Changing the password Secret updates the hash.
	pw := &corev1.Secret{}
	must(t, get(pw, f.ns, "dev1-password"))
	pw.Data = map[string][]byte{PasswordKey: []byte("second-password")}
	must(t, k8sClient.Update(testCtx, pw))
	eventually(t, func() error {
		h, err := hashOf()
		if err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(h), []byte("second-password")) != nil {
			return fmt.Errorf("hash not updated")
		}
		return nil
	})

	// Removing the reference disables password login.
	updateEnv(t, env, func() {
		env.Spec.PasswordSecretName = ""
	})
	eventually(t, func() error {
		h, err := hashOf()
		if err != nil {
			return err
		}
		if h != "" {
			return fmt.Errorf("hash still set")
		}
		return nil
	})
}

// updateEnv re-reads env, applies mutate and updates, retrying on conflicts
// with the controller.
func updateEnv(t *testing.T, env *devenvv1.DevEnvironment, mutate func()) {
	t.Helper()
	must(t, retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := k8sClient.Get(testCtx, client.ObjectKeyFromObject(env), env); err != nil {
			return err
		}
		mutate()
		return k8sClient.Update(testCtx, env)
	}))
}

func TestImagePrepull(t *testing.T) {
	ds := &appsv1.DaemonSet{}
	getDS := func() error { return get(ds, "devenv-system", PrepullName) }

	must(t, k8sClient.Create(testCtx, &devenvv1.DevImage{
		ObjectMeta: metav1.ObjectMeta{Name: "prepull-a"},
		Spec:       devenvv1.DevImageSpec{Image: "example.com/a:1", Prepull: true},
	}))
	must(t, k8sClient.Create(testCtx, &devenvv1.DevImage{
		ObjectMeta: metav1.ObjectMeta{Name: "prepull-b"},
		Spec:       devenvv1.DevImageSpec{Image: "example.com/b:1", Prepull: true},
	}))
	eventually(t, func() error {
		if err := getDS(); err != nil {
			return err
		}
		if n := len(ds.Spec.Template.Spec.Volumes); n != 2 {
			return fmt.Errorf("volumes = %d", n)
		}
		return nil
	})
	v := ds.Spec.Template.Spec.Volumes
	if v[0].Image == nil || v[0].Image.Reference != "example.com/a:1" || v[1].Image.Reference != "example.com/b:1" {
		t.Errorf("volumes = %+v", v)
	}
	c := ds.Spec.Template.Spec.Containers[0]
	if c.Image != "devenv:test" || c.Command[1] != "sleep" {
		t.Errorf("container = %+v", c)
	}

	// Disabling prepull on all images removes the DaemonSet.
	for _, n := range []string{"prepull-a", "prepull-b"} {
		img := &devenvv1.DevImage{}
		must(t, get(img, "", n))
		img.Spec.Prepull = false
		must(t, k8sClient.Update(testCtx, img))
	}
	eventually(t, func() error {
		if err := getDS(); !apierrors.IsNotFound(err) {
			return fmt.Errorf("daemonset still exists: %v", err)
		}
		return nil
	})
}
