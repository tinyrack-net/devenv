package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

func qty(s string) *resource.Quantity {
	q := resource.MustParse(s)
	return &q
}

// fixture registers an image and a class unique to the test. Environment
// names are prefixed with the test name (all tests share one namespace).
type fixture struct {
	t      *testing.T
	prefix string
	image  string
	class  string
}

func newFixture(t *testing.T, classSpec model.ClassSpec, desktop bool) fixture {
	t.Helper()
	name := strings.ToLower(strings.NewReplacer("/", "-", "_", "-").Replace(strings.TrimPrefix(t.Name(), "Test")))
	f := fixture{t: t, prefix: name, image: name + "-img", class: name + "-class"}
	testCat.SetImage(f.image, model.ImageSpec{
		Image:            "ghcr.io/tinyrack-net/devenv-base:latest",
		Desktop:          desktop,
		DefaultResources: model.Resources{CPU: qty("2"), Memory: qty("4Gi")},
	})
	testCat.SetClass(f.class, classSpec)
	return f
}

func (f fixture) name(n string) string { return f.prefix + "-" + n }

// create inserts an environment record named prefix-n.
func (f fixture) create(n string, mutate ...func(*model.Environment)) *model.Environment {
	f.t.Helper()
	e := &model.Environment{
		Name: f.name(n),
		Spec: model.EnvironmentSpec{
			ImageRef:          f.image,
			ClassName:         f.class,
			SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA test@example"},
		},
	}
	for _, m := range mutate {
		m(e)
	}
	must(f.t, testDB.CreateEnvironment(testCtx, e))
	return e
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(obj client.Object, name string) error {
	return k8sClient.Get(testCtx, types.NamespacedName{Namespace: testNS, Name: name}, obj)
}

func load(t *testing.T, name string) *model.Environment {
	t.Helper()
	e, err := testDB.GetEnvironment(testCtx, name)
	must(t, err)
	return e
}

func waitPhase(t *testing.T, name string, phase model.Phase) *model.Environment {
	t.Helper()
	var cur *model.Environment
	eventually(t, func() error {
		var err error
		if cur, err = testDB.GetEnvironment(testCtx, name); err != nil {
			return err
		}
		if cur.Status.Phase != phase {
			return fmt.Errorf("phase %q (%s), want %q", cur.Status.Phase, cur.Status.Message, phase)
		}
		return nil
	})
	return cur
}

// update changes an environment record (spec), retrying on conflicts.
func update(t *testing.T, name string, mutate func(*model.Environment)) {
	t.Helper()
	_, err := testDB.MutateEnvironment(testCtx, name, func(e *model.Environment) error { mutate(e); return nil })
	must(t, err)
}

func TestCreatesChildren(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, true)
	env := f.create("dev1")
	n := env.Name

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	if len(pod.OwnerReferences) != 0 {
		t.Errorf("pod has owner references %v", pod.OwnerReferences)
	}
	c := pod.Spec.Containers[0]
	if got := c.Resources.Limits.Cpu().String(); got != "2" {
		t.Errorf("cpu limit = %s, want image default 2", got)
	}
	if got := c.Resources.Requests.Memory().String(); got != "4Gi" {
		t.Errorf("memory request = %s, want 4Gi", got)
	}
	if pod.Spec.Volumes[0].PersistentVolumeClaim == nil || pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != n+"-home" {
		t.Errorf("home volume not bound to pvc: %+v", pod.Spec.Volumes[0])
	}
	if pod.Annotations[AnnotationSpecHash] == "" {
		t.Errorf("missing spec hash annotation")
	}

	pvc := &corev1.PersistentVolumeClaim{}
	must(t, get(pvc, n+"-home"))
	if got := pvc.Spec.Resources.Requests.Storage().String(); got != model.DefaultDisk {
		t.Errorf("pvc size = %s, want %s", got, model.DefaultDisk)
	}
	if !load(t, n).VolumeCreated {
		t.Error("volumeCreated not recorded")
	}

	secret := &corev1.Secret{}
	must(t, get(secret, n+"-ssh"))
	if got := string(secret.Data["authorized_keys"]); got != "ssh-ed25519 AAAA test@example\n" {
		t.Errorf("authorized_keys = %q", got)
	}
	hostKey := string(secret.Data[HostKeyFile])
	if !strings.Contains(hostKey, "OPENSSH PRIVATE KEY") || !strings.HasPrefix(string(secret.Data[HostPublicKeyFile]), "ssh-ed25519 ") {
		t.Errorf("host key not generated: %v", secret.Data)
	}
	if string(load(t, n).HostKey) != hostKey {
		t.Error("host key not stored in the database")
	}

	// Updating keys keeps the host key.
	update(t, n, func(e *model.Environment) { e.Spec.SSHAuthorizedKeys = []string{"ssh-ed25519 BBBB other@example"} })
	eventually(t, func() error {
		s := &corev1.Secret{}
		if err := get(s, n+"-ssh"); err != nil {
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
	must(t, get(svc, n))
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != SSHPort {
		t.Errorf("only ssh should be exposed (vnc is tunneled), got %+v", svc.Spec.Ports)
	}

	cur := waitPhase(t, n, model.PhaseStarting)
	if r := cur.Status.Resources; r.CPU == nil || r.CPU.String() != "2" || r.Memory.String() != "4Gi" || r.Disk.String() != model.DefaultDisk {
		t.Errorf("status resources = %+v, want image defaults", r)
	}
	if cur.Status.Image != "ghcr.io/tinyrack-net/devenv-base:latest" || !cur.Status.Desktop || cur.Status.ObservedGeneration != cur.Generation {
		t.Errorf("status not populated: %+v", cur.Status)
	}
}

func TestEphemeralStorage(t *testing.T) {
	f := newFixture(t, model.ClassSpec{Storage: model.StorageSpec{Mode: model.StorageModeEphemeral}}, false)
	n := f.create("dev1").Name

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	if pod.Spec.Volumes[0].EmptyDir == nil {
		t.Errorf("expected emptyDir home volume")
	}
	if err := get(&corev1.PersistentVolumeClaim{}, n+"-home"); !apierrors.IsNotFound(err) {
		t.Errorf("expected no pvc, got err=%v", err)
	}
}

func TestStopAndStart(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, false)
	n := f.create("dev1").Name
	eventually(t, func() error { return get(&corev1.Pod{}, n) })

	// Stop: pod goes away, pvc stays.
	update(t, n, func(e *model.Environment) { e.Spec.Running = ptr.To(false) })
	waitPhase(t, n, model.PhaseStopped)
	if err := get(&corev1.Pod{}, n); !apierrors.IsNotFound(err) {
		t.Errorf("pod should be deleted, err=%v", err)
	}
	must(t, get(&corev1.PersistentVolumeClaim{}, n+"-home"))

	update(t, n, func(e *model.Environment) { e.Spec.Running = ptr.To(true) })
	eventually(t, func() error { return get(&corev1.Pod{}, n) })
}

func TestSpecChangeRecreatesPod(t *testing.T) {
	scName := "expandable"
	must(t, client.IgnoreAlreadyExists(k8sClient.Create(testCtx, &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: scName},
		Provisioner:          "example.com/fake",
		AllowVolumeExpansion: ptr.To(true),
	})))
	f := newFixture(t, model.ClassSpec{Storage: model.StorageSpec{StorageClassName: &scName}}, false)
	n := f.create("dev1").Name

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	oldUID := pod.UID

	// envtest has no provisioner; mark the claim bound so resizing is allowed.
	pvc := &corev1.PersistentVolumeClaim{}
	must(t, get(pvc, n+"-home"))
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = pvc.Spec.Resources.Requests
	must(t, k8sClient.Status().Update(testCtx, pvc))

	update(t, n, func(e *model.Environment) {
		e.Spec.Resources.CPU = qty("3")
		e.Spec.Resources.Disk = qty("20Gi")
	})
	eventually(t, func() error {
		p := &corev1.Pod{}
		if err := get(p, n); err != nil {
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
		if err := get(pvc, n+"-home"); err != nil {
			return err
		}
		if got := pvc.Spec.Resources.Requests.Storage().String(); got != "20Gi" {
			return fmt.Errorf("pvc size = %s", got)
		}
		return nil
	})
}

func TestClassLimits(t *testing.T) {
	f := newFixture(t, model.ClassSpec{Limits: model.Limits{MaxCPU: qty("1")}}, false)
	n := f.create("dev1").Name // image default cpu 2 > max 1
	cur := waitPhase(t, n, model.PhaseFailed)
	if !strings.Contains(cur.Status.Message, "exceeds class limit") {
		t.Errorf("message = %q", cur.Status.Message)
	}
	if err := get(&corev1.Pod{}, n); !apierrors.IsNotFound(err) {
		t.Errorf("pod should not be created, err=%v", err)
	}
}

func TestMissingImageThenCreated(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, false)
	late := f.image + "-late"
	n := f.create("dev1", func(e *model.Environment) { e.Spec.ImageRef = late }).Name
	waitPhase(t, n, model.PhasePending)
	// A console created image (database) appears.
	must(t, testDB.CreateImage(testCtx, &model.Image{Name: late, Spec: model.ImageSpec{Image: "example.com/late:1"}}))
	eventually(t, func() error { return get(&corev1.Pod{}, n) })
}

func TestPodOverrides(t *testing.T) {
	f := newFixture(t, model.ClassSpec{
		PodOverrides: json.RawMessage(`{"metadata":{"labels":{"extra":"yes","devenv.tinyrack.net/environment":"hijack"}},"spec":{"hostUsers":false}}`),
	}, false)
	n := f.create("dev1").Name
	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	if pod.Labels["extra"] != "yes" {
		t.Errorf("override label missing")
	}
	if pod.Labels[LabelEnvironment] != n {
		t.Errorf("identity label must not be overridden, got %q", pod.Labels[LabelEnvironment])
	}
	if pod.Spec.HostUsers == nil || *pod.Spec.HostUsers {
		t.Errorf("hostUsers override not applied")
	}
}

func TestLoadBalancerMode(t *testing.T) {
	f := newFixture(t, model.ClassSpec{
		Network: model.NetworkSpec{
			Mode: model.NetworkModeLoadBalancer,
			LoadBalancer: &model.LoadBalancerSpec{
				Labels:      map[string]string{"pool": "lan"},
				Annotations: map[string]string{"lb.example/ip": "auto"},
			},
		},
	}, false)
	n := f.create("dev1", func(e *model.Environment) {
		e.Spec.Ports = []model.Port{{Port: 8080, Protocol: "TCP"}, {Port: 22, Protocol: "TCP"}, {Port: 53, Protocol: "UDP"}}
	}).Name

	svc := &corev1.Service{}
	eventually(t, func() error { return get(svc, n+"-lb") })
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || svc.Labels["pool"] != "lan" || svc.Annotations["lb.example/ip"] != "auto" {
		t.Errorf("service = %+v", svc.ObjectMeta)
	}
	if len(svc.Spec.Ports) != 3 {
		t.Errorf("ports = %+v (ssh, 8080/tcp, 53/udp expected)", svc.Spec.Ports)
	}

	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}}
	must(t, k8sClient.Status().Update(testCtx, svc))
	eventually(t, func() error {
		cur := load(t, n)
		if cur.Status.IP != "192.0.2.10" || cur.Status.NetworkMode != model.NetworkModeLoadBalancer {
			return fmt.Errorf("status ip=%q mode=%q", cur.Status.IP, cur.Status.NetworkMode)
		}
		return nil
	})

	// Stopping keeps the address.
	update(t, n, func(e *model.Environment) { e.Spec.Running = ptr.To(false) })
	cur := waitPhase(t, n, model.PhaseStopped)
	if cur.Status.IP != "192.0.2.10" {
		t.Errorf("ip lost on stop: %q", cur.Status.IP)
	}
	must(t, get(&corev1.Service{}, n+"-lb"))
}

func TestMultusMode(t *testing.T) {
	f := newFixture(t, model.ClassSpec{
		Network: model.NetworkSpec{
			Mode:   model.NetworkModeMultus,
			Multus: &model.MultusSpec{NetworkAttachment: "kube-system/lan", StableMAC: true},
		},
	}, false)
	n := f.create("dev1").Name

	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	var sel []multusSelection
	if err := json.Unmarshal([]byte(pod.Annotations[annotationNetworks]), &sel); err != nil || len(sel) != 1 {
		t.Fatalf("networks annotation = %q", pod.Annotations[annotationNetworks])
	}
	if sel[0].Namespace != "kube-system" || sel[0].Name != "lan" || sel[0].Interface != "net1" || sel[0].MAC != nameMAC(n) {
		t.Errorf("selection = %+v", sel[0])
	}
	if mac, _ := net.ParseMAC(sel[0].MAC); mac[0]&0x02 == 0 || mac[0]&0x01 != 0 {
		t.Errorf("mac %s must be locally administered unicast", sel[0].MAC)
	}
	eventually(t, func() error {
		if m := load(t, n).Status.StableMAC; m != sel[0].MAC {
			return fmt.Errorf("pinned MAC %q", m)
		}
		return nil
	})

	pod.Annotations[annotationNetworkStatus] = `[{"name":"cilium","interface":"eth0","ips":["10.0.0.5"],"default":true},` +
		`{"name":"kube-system/lan","interface":"net1","ips":["fe80::1","192.168.1.50"],"mac":"` + sel[0].MAC + `"}]`
	must(t, k8sClient.Update(testCtx, pod))
	eventually(t, func() error {
		cur := load(t, n)
		if cur.Status.IP != "192.168.1.50" || cur.Status.MAC != sel[0].MAC {
			return fmt.Errorf("ip=%q mac=%q", cur.Status.IP, cur.Status.MAC)
		}
		return nil
	})
	if err := get(&corev1.Service{}, n+"-lb"); !apierrors.IsNotFound(err) {
		t.Errorf("no lb service expected in multus mode: %v", err)
	}

	// A MAC pinned earlier (e.g. migrated from the UID based MAC) wins.
	m := f.create("dev2", func(e *model.Environment) { e.Status.StableMAC = "02:aa:bb:cc:dd:ee" }).Name
	eventually(t, func() error {
		p := &corev1.Pod{}
		if err := get(p, m); err != nil {
			return err
		}
		if !strings.Contains(p.Annotations[annotationNetworks], "02:aa:bb:cc:dd:ee") {
			return fmt.Errorf("networks %s", p.Annotations[annotationNetworks])
		}
		return nil
	})
}

func TestPassword(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, false)
	hash, err := HashPassword("first-password")
	must(t, err)
	n := f.create("dev1", func(e *model.Environment) { e.PasswordHash = hash }).Name

	hashOf := func() (string, error) {
		s := &corev1.Secret{}
		if err := get(s, n+"-ssh"); err != nil {
			return "", err
		}
		return string(s.Data[PasswordHashFile]), nil
	}
	eventually(t, func() error {
		if h, err := hashOf(); err != nil || h != hash {
			return fmt.Errorf("hash %q %v", h, err)
		}
		if !load(t, n).Status.PasswordSet {
			return fmt.Errorf("passwordSet false")
		}
		return nil
	})
	update(t, n, func(e *model.Environment) { e.PasswordHash = "" })
	eventually(t, func() error {
		if h, err := hashOf(); err != nil || h != "" {
			return fmt.Errorf("hash still set %q %v", h, err)
		}
		return nil
	})
}

func TestImagePrepull(t *testing.T) {
	ds := &appsv1.DaemonSet{}
	getDS := func() error {
		return k8sClient.Get(testCtx, types.NamespacedName{Namespace: "devenv-system", Name: PrepullName}, ds)
	}
	for _, n := range []string{"prepull-a", "prepull-b"} {
		must(t, testDB.CreateImage(testCtx, &model.Image{Name: n, Spec: model.ImageSpec{Image: "example.com/" + strings.TrimPrefix(n, "prepull-") + ":1", Prepull: true}}))
	}
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
	for _, n := range []string{"prepull-a", "prepull-b"} {
		img, err := testDB.GetImage(testCtx, n)
		must(t, err)
		img.Spec.Prepull = false
		must(t, testDB.UpdateImage(testCtx, img))
	}
	eventually(t, func() error {
		if err := getDS(); !apierrors.IsNotFound(err) {
			return fmt.Errorf("daemonset still exists: %v", err)
		}
		return nil
	})
}

func TestUserKeys(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, false)
	owner := &model.User{Name: f.name("owner"), Spec: model.UserSpec{SSHKeys: []model.UserSSHKey{{Name: "a", PublicKey: "ssh-ed25519 OWNR owner"}}}}
	friend := &model.User{Name: f.name("friend"), Spec: model.UserSpec{SSHKeys: []model.UserSSHKey{{Name: "b", PublicKey: "ssh-ed25519 FRND friend"}}},
		Status: model.UserStatus{Groups: []string{"team-" + f.prefix}}}
	must(t, testDB.CreateUser(testCtx, owner))
	must(t, testDB.CreateUser(testCtx, friend))

	n := f.create("keys", func(e *model.Environment) { e.Spec.Owner = owner.Name }).Name
	keysContain := func(want string, present bool) {
		t.Helper()
		eventually(t, func() error {
			s := &corev1.Secret{}
			if err := get(s, n+"-ssh"); err != nil {
				return err
			}
			if strings.Contains(string(s.Data[AuthorizedKeysFile]), want) != present {
				return fmt.Errorf("authorized_keys %q: %s present=%v", s.Data[AuthorizedKeysFile], want, !present)
			}
			return nil
		})
	}
	keysContain("OWNR", true)
	keysContain("FRND", false)

	update(t, n, func(e *model.Environment) { e.Spec.Sharing.Groups = []string{"team-" + f.prefix} })
	keysContain("FRND", true)

	// Changing a user's keys updates environments without touching them.
	_, err := testDB.MutateUser(testCtx, owner.Name, func(u *model.User) error {
		u.Spec.SSHKeys = append(u.Spec.SSHKeys, model.UserSSHKey{Name: "c", PublicKey: "ssh-ed25519 NEWK owner2"})
		return nil
	})
	must(t, err)
	keysContain("NEWK", true)

	_, err = testDB.MutateUser(testCtx, friend.Name, func(u *model.User) error { u.Spec.Disabled = true; return nil })
	must(t, err)
	keysContain("FRND", false)
}

func TestChangeClass(t *testing.T) {
	sc := "standard"
	f := newFixture(t, model.ClassSpec{Storage: model.StorageSpec{StorageClassName: &sc}}, false)
	lbClass := f.class + "-lb"
	testCat.SetClass(lbClass, model.ClassSpec{Storage: model.StorageSpec{StorageClassName: &sc}, Network: model.NetworkSpec{Mode: model.NetworkModeLoadBalancer}})
	other := "other"
	badClass := f.class + "-other-storage"
	testCat.SetClass(badClass, model.ClassSpec{Storage: model.StorageSpec{StorageClassName: &other}})

	n := f.create("mv").Name
	pod := &corev1.Pod{}
	eventually(t, func() error { return get(pod, n) })
	pvc := &corev1.PersistentVolumeClaim{}
	must(t, get(pvc, n+"-home"))
	oldPod, oldPVC := pod.UID, pvc.UID

	update(t, n, func(e *model.Environment) { e.Spec.ClassName = lbClass })
	eventually(t, func() error { return get(&corev1.Service{}, n+"-lb") })
	must(t, get(pod, n))
	if pod.UID != oldPod || !pod.DeletionTimestamp.IsZero() {
		t.Fatal("pod restarted although its spec did not change")
	}

	update(t, n, func(e *model.Environment) { e.Spec.ClassName = f.class })
	eventually(t, func() error {
		if err := get(&corev1.Service{}, n+"-lb"); !apierrors.IsNotFound(err) {
			return fmt.Errorf("lb service still present: %v", err)
		}
		return nil
	})

	update(t, n, func(e *model.Environment) { e.Spec.ClassName = badClass })
	cur := waitPhase(t, n, model.PhaseFailed)
	if !strings.Contains(cur.Status.Message, "storage class") {
		t.Fatalf("message %q", cur.Status.Message)
	}
	must(t, get(pvc, n+"-home"))
	if pvc.UID != oldPVC {
		t.Fatal("home volume was replaced")
	}
}

// TestRecovery simulates disaster recovery: the database comes back (with
// host key and MAC) before the storage system restored the home volume. The
// environment waits instead of starting empty, then adopts the restored
// volume, including PVCs still owned by a 0.10 DevEnvironment.
func TestRecovery(t *testing.T) {
	f := newFixture(t, model.ClassSpec{Storage: model.StorageSpec{Labels: map[string]string{"backup": "enabled"}}}, false)
	priv, pub, err := generateHostKey()
	must(t, err)
	n := f.create("rs", func(e *model.Environment) {
		e.HostKey, e.HostKeyPub, e.VolumeCreated = priv, pub, true
	}).Name

	cur := waitPhase(t, n, model.PhasePending)
	if cur.Status.Reason != ReasonAwaitingVolume {
		t.Fatalf("reason %q", cur.Status.Reason)
	}
	if err := get(&corev1.PersistentVolumeClaim{}, n+"-home"); !apierrors.IsNotFound(err) {
		t.Fatalf("empty volume created while waiting: %v", err)
	}
	if err := get(&corev1.Pod{}, n); !apierrors.IsNotFound(err) {
		t.Fatalf("pod started without its volume: %v", err)
	}

	// The storage system restores the volume (labels of the old version and
	// an owner reference to the old CRD object).
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: n + "-home", Namespace: testNS,
			Labels:          map[string]string{LabelEnvironment: n, "restored": "yes"},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "devenv.tinyrack.net/v1alpha1", Kind: "DevEnvironment", Name: n, UID: "00000000-0000-0000-0000-000000000001"}},
		},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")}}},
	}
	must(t, k8sClient.Create(testCtx, pvc))
	eventually(t, func() error { return get(&corev1.Pod{}, n) })
	eventually(t, func() error {
		p := &corev1.PersistentVolumeClaim{}
		if err := get(p, n+"-home"); err != nil {
			return err
		}
		if p.UID != pvc.UID || len(p.OwnerReferences) != 0 || p.Labels["backup"] != "enabled" || p.Labels["restored"] != "yes" {
			return fmt.Errorf("pvc %v %v", p.OwnerReferences, p.Labels)
		}
		return nil
	})
	s := &corev1.Secret{}
	must(t, get(s, n+"-ssh"))
	if string(s.Data[HostPublicKeyFile]) != string(pub) {
		t.Fatal("host key not restored from the database")
	}
}

func TestDeleteAndOrphans(t *testing.T) {
	f := newFixture(t, model.ClassSpec{}, false)
	n := f.create("del").Name
	eventually(t, func() error { return get(&corev1.Pod{}, n) })

	_, err := testDB.MutateEnvironment(testCtx, n, func(e *model.Environment) error { e.Deleting = true; return nil })
	must(t, err)
	// envtest has no kubelet: pods stay terminating forever unless forced.
	eventually(t, func() error {
		p := &corev1.Pod{}
		if err := get(p, n); err == nil {
			_ = k8sClient.Delete(testCtx, p, client.GracePeriodSeconds(0))
			return fmt.Errorf("pod still there")
		}
		return nil
	})
	eventually(t, func() error {
		if _, err := testDB.GetEnvironment(testCtx, n); !errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("record still there: %v", err)
		}
		return nil
	})
	for _, o := range []client.Object{&corev1.Service{}, &corev1.Secret{}, &corev1.PersistentVolumeClaim{}} {
		name := map[string]string{"*v1.Service": n, "*v1.Secret": n + "-ssh", "*v1.PersistentVolumeClaim": n + "-home"}[fmt.Sprintf("%T", o)]
		if err := get(o, name); err == nil && o.GetDeletionTimestamp().IsZero() {
			t.Errorf("%T %s not deleted", o, name)
		}
	}

	// Objects without a record are reported, never deleted.
	ghost := f.name("ghost")
	must(t, k8sClient.Create(testCtx, &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: ghost + "-home", Namespace: testNS, Labels: map[string]string{LabelEnvironment: ghost, LabelManagedBy: ManagedByValue}},
		Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}}},
	}))
	orphans, err := Orphans(testCtx, k8sClient, testDB, testNS)
	must(t, err)
	if len(orphans[ghost]) != 1 {
		t.Fatalf("orphans %v", orphans)
	}
	must(t, get(&corev1.PersistentVolumeClaim{}, ghost+"-home"))
}

func TestStableMACFromName(t *testing.T) {
	a := stableMAC(&model.Environment{Name: "x"})
	b := stableMAC(&model.Environment{Name: "x", CreatedAt: metav1.Now().Time})
	if a != b || a == stableMAC(&model.Environment{Name: "y"}) {
		t.Fatal("MAC must depend on the name only")
	}
	if stableMAC(&model.Environment{Name: "x", Status: model.EnvironmentStatus{StableMAC: "02:00:00:00:00:01"}}) != "02:00:00:00:00:01" {
		t.Fatal("pinned MAC ignored")
	}
}
