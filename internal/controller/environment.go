package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/tinyrack-net/devenv/internal/catalog"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/store"
)

// ReasonAwaitingVolume is the phase reason of environments whose home volume
// has not been restored yet.
const ReasonAwaitingVolume = "AwaitingVolume"

// resyncInterval re-checks every environment (covers missed changes and
// Kubernetes objects deleted while the controller was down).
const resyncInterval = 5 * time.Minute

// EnvironmentReconciler makes the Kubernetes objects of every environment in
// the database match it. Requests are environment names (Namespace empty).
type EnvironmentReconciler struct {
	Client    client.Client
	Recorder  events.EventRecorder
	DB        *store.Store
	Catalog   *catalog.Catalog
	Namespace string
	// GatewayAuthorizedKey is the public key the devenv server uses to connect
	// to environments (SSH gateway, web terminal, VNC tunnel). Optional.
	GatewayAuthorizedKey string
	// HelperImage is mounted read-only at /opt/devenv as an image volume to
	// provide devenv-agent. Empty disables it.
	HelperImage string
	// HelperImagePullPolicy for the helper image volume.
	HelperImagePullPolicy corev1.PullPolicy
}

// +kubebuilder:rbac:groups="",resources=pods;services;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *EnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	name := req.Name
	env, err := r.DB.GetEnvironment(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		// Objects without a database record are left alone: the database
		// may have been restored to an earlier point. Admins clean them up.
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if env.Deleting {
		return r.finalize(ctx, env)
	}

	orig := env.Status
	res, err := r.reconcile(ctx, env)
	env.Status.ObservedGeneration = env.Generation
	if !apiequality.Semantic.DeepEqual(orig, env.Status) {
		if uerr := r.DB.UpdateEnvironmentStatus(ctx, name, env.Status); uerr != nil && !errors.Is(uerr, store.ErrNotFound) {
			logf.FromContext(ctx).Error(uerr, "update status")
			if err == nil {
				err = uerr
			}
		}
	}
	return res, err
}

// event records a Kubernetes event on the environment's pod (or the object
// given) so `kubectl get events` and the console show it.
func (r *EnvironmentReconciler) event(env *model.Environment, obj client.Object, typ, reason, format string, args ...any) {
	if r.Recorder == nil {
		return
	}
	if obj == nil {
		obj = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(env), Namespace: r.Namespace}}
	}
	r.Recorder.Eventf(obj, nil, typ, reason, reason, format, args...)
}

func (r *EnvironmentReconciler) reconcile(ctx context.Context, env *model.Environment) (ctrl.Result, error) {
	rs, msg, err := r.resolve(ctx, env)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rs == nil {
		setPhase(env, model.PhasePending, "Unresolved", msg)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	env.Status.Image = rs.image.Spec.Image
	env.Status.Desktop = rs.image.Spec.Desktop
	eff := rs.resources()
	env.Status.Resources = model.Resources{CPU: &eff.CPU, Memory: &eff.Memory}
	if rs.storageMode() == model.StorageModePersistent {
		env.Status.Resources.Disk = &eff.Disk
	}
	env.Status.PasswordSet = env.PasswordHash != ""

	if err := rs.validate(); err != nil {
		setPhase(env, model.PhaseFailed, "InvalidSpec", err.Error())
		return ctrl.Result{}, nil
	}
	if env.Status.NetworkMode != "" && env.Status.NetworkMode != rs.networkMode() {
		// Moved to a class with another network: the old address is gone.
		env.Status.IP, env.Status.MAC = "", ""
	}
	env.Status.NetworkMode = rs.networkMode()
	if m := rs.class.Spec.Network.Multus; rs.networkMode() == model.NetworkModeMultus && m != nil && m.StableMAC && env.Status.StableMAC == "" {
		env.Status.StableMAC = stableMAC(env)
	}

	if err := r.ensureHostKey(ctx, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("host key: %w", err)
	}
	if err := r.ensureSecret(ctx, rs); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure secret: %w", err)
	}
	if rs.storageMode() == model.StorageModePersistent {
		res, done, err := r.ensurePVC(ctx, rs)
		if err != nil || done {
			return res, err
		}
	}
	if err := r.ensureService(ctx, rs); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure service: %w", err)
	}
	env.Status.ServiceName = serviceName(env)
	if err := r.ensureLBService(ctx, rs); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure loadbalancer service: %w", err)
	}

	if !env.IsRunning() {
		return r.ensureStopped(ctx, env)
	}
	return r.ensureRunning(ctx, rs)
}

// resolve loads the referenced image and class. A nil result with a message
// means a dependency is missing.
func (r *EnvironmentReconciler) resolve(ctx context.Context, env *model.Environment) (*resolved, string, error) {
	image, err := r.Catalog.Image(ctx, env.Spec.ImageRef)
	if errors.Is(err, catalog.ErrNotFound) {
		return nil, fmt.Sprintf("image %q not found", env.Spec.ImageRef), nil
	}
	if err != nil {
		return nil, "", err
	}
	class, err := r.Catalog.Class(env.ClassName())
	if errors.Is(err, catalog.ErrNotFound) {
		return nil, fmt.Sprintf("class %q not found", env.ClassName()), nil
	}
	if err != nil {
		return nil, "", err
	}
	return &resolved{ns: r.Namespace, env: env, image: image, class: class}, "", nil
}

// ensureHostKey generates the environment's SSH host key once and keeps it
// in the database, so it survives the loss of the cluster.
func (r *EnvironmentReconciler) ensureHostKey(ctx context.Context, env *model.Environment) error {
	if len(env.HostKey) > 0 && len(env.HostKeyPub) > 0 {
		return nil
	}
	priv, pub, err := generateHostKey()
	if err != nil {
		return err
	}
	updated, err := r.DB.MutateEnvironment(ctx, env.Name, func(e *model.Environment) error {
		if len(e.HostKey) == 0 || len(e.HostKeyPub) == 0 {
			e.HostKey, e.HostKeyPub = priv, pub
		}
		return nil
	})
	if err != nil {
		return err
	}
	env.HostKey, env.HostKeyPub, env.Revision = updated.HostKey, updated.HostKeyPub, updated.Revision
	return nil
}


func (r *EnvironmentReconciler) ensureSecret(ctx context.Context, rs *resolved) error {
	users, err := r.DB.ListUsers(ctx)
	if err != nil {
		return err
	}
	extra := model.AccessKeys(users, rs.env)
	if r.GatewayAuthorizedKey != "" {
		extra = append(extra, r.GatewayAuthorizedKey)
	}
	desired := rs.desiredSecret(extra...)
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = desired.Labels
		obj.Type = desired.Type
		obj.Data = desired.Data
		obj.Data[HostKeyFile] = rs.env.HostKey
		obj.Data[HostPublicKeyFile] = rs.env.HostKeyPub
		obj.Data[PasswordHashFile] = []byte(rs.env.PasswordHash)
		obj.OwnerReferences = nil
		return nil
	})
	return err
}

// ensurePVC creates or updates the home volume. done reports that the
// environment must wait (volume not restored yet, storage mismatch).
func (r *EnvironmentReconciler) ensurePVC(ctx context.Context, rs *resolved) (ctrl.Result, bool, error) {
	env := rs.env
	desired := rs.desiredPVC()
	obj := &corev1.PersistentVolumeClaim{}
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(desired), obj)
	if apierrors.IsNotFound(err) {
		if env.VolumeCreated {
			// The database knows a volume that is not there: it has not
			// been restored yet. Never start with an empty home directory.
			env.Status.PodName, env.Status.NodeName = "", ""
			if pod := (&corev1.Pod{}); r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: podName(env)}, pod) == nil {
				_ = r.Client.Delete(ctx, pod)
			}
			setPhase(env, model.PhasePending, ReasonAwaitingVolume, "waiting for the home volume "+pvcName(env)+" to be restored; an administrator can reset the volume to start empty")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, true, nil
		}
		if err := r.Client.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, true, fmt.Errorf("create pvc: %w", err)
		}
		return ctrl.Result{}, false, r.markVolumeCreated(ctx, env)
	}
	if err != nil {
		return ctrl.Result{}, true, err
	}
	if !env.VolumeCreated {
		// Adopted (migration, or a volume restored before the record).
		if err := r.markVolumeCreated(ctx, env); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	// Keep labels and annotations in sync; drop owner references of
	// earlier versions (CRD owners) so nothing garbage collects the volume.
	before := obj.DeepCopy()
	for k, v := range desired.Labels {
		if obj.Labels == nil {
			obj.Labels = map[string]string{}
		}
		obj.Labels[k] = v
	}
	for k, v := range desired.Annotations {
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		obj.Annotations[k] = v
	}
	obj.OwnerReferences = withoutDevenvOwners(obj.OwnerReferences)
	if !apiequality.Semantic.DeepEqual(before.ObjectMeta, obj.ObjectMeta) {
		if err := r.Client.Patch(ctx, obj, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, true, err
		}
	}
	// The class may have changed; the home volume can not move to another
	// storage class.
	haveSC, wantSC := obj.Spec.StorageClassName, desired.Spec.StorageClassName
	if wantSC != nil && (haveSC == nil || *haveSC != *wantSC) {
		setPhase(env, model.PhaseFailed, "StorageMismatch", fmt.Sprintf("home volume uses storage class %s but class %q wants %s; move back to a class with the same storage", strOr(haveSC, "(cluster default)"), rs.class.Name, *wantSC))
		return ctrl.Result{}, true, nil
	}
	// Only growth is supported.
	want := desired.Spec.Resources.Requests[corev1.ResourceStorage]
	have := obj.Spec.Resources.Requests[corev1.ResourceStorage]
	if want.Cmp(have) > 0 {
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.Resources.Requests[corev1.ResourceStorage] = want
		if err := r.Client.Patch(ctx, obj, patch); err != nil {
			logf.FromContext(ctx).Info("resize home volume failed", "error", err.Error())
			r.event(env, obj, corev1.EventTypeWarning, "ResizeFailed", "resize home volume: %v", err)
		}
	}
	return ctrl.Result{}, false, nil
}

func (r *EnvironmentReconciler) markVolumeCreated(ctx context.Context, env *model.Environment) error {
	updated, err := r.DB.MutateEnvironment(ctx, env.Name, func(e *model.Environment) error {
		e.VolumeCreated = true
		return nil
	})
	if err != nil {
		return err
	}
	env.VolumeCreated, env.Revision = true, updated.Revision
	return nil
}

// withoutDevenvOwners drops DevEnvironment owner references (0.10 and
// earlier).
func withoutDevenvOwners(refs []metav1.OwnerReference) []metav1.OwnerReference {
	var out []metav1.OwnerReference
	for _, ref := range refs {
		if ref.Kind != "DevEnvironment" {
			out = append(out, ref)
		}
	}
	return out
}

func (r *EnvironmentReconciler) ensureService(ctx context.Context, rs *resolved) error {
	desired := rs.desiredService()
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = desired.Labels
		obj.Spec.Type = desired.Spec.Type
		obj.Spec.Selector = desired.Spec.Selector
		obj.Spec.Ports = desired.Spec.Ports
		obj.OwnerReferences = nil
		return nil
	})
	return err
}

// ensureLBService manages the LoadBalancer Service in loadBalancer mode and
// removes it otherwise. The Service survives stop so the address is kept.
func (r *EnvironmentReconciler) ensureLBService(ctx context.Context, rs *resolved) error {
	env := rs.env
	if rs.networkMode() != model.NetworkModeLoadBalancer {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: lbServiceName(env), Namespace: r.Namespace}}
		if err := r.Client.Delete(ctx, svc); client.IgnoreNotFound(err) != nil {
			return err
		}
		if rs.networkMode() == model.NetworkModeGateway {
			env.Status.IP = ""
		}
		return nil
	}
	desired := rs.desiredLBService()
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = desired.Labels
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		for k, v := range desired.Annotations {
			obj.Annotations[k] = v
		}
		obj.Spec.Type = desired.Spec.Type
		if obj.Spec.LoadBalancerClass == nil {
			// Immutable once set.
			obj.Spec.LoadBalancerClass = desired.Spec.LoadBalancerClass
		}
		obj.Spec.Selector = desired.Spec.Selector
		obj.Spec.Ports = mergeNodePorts(obj.Spec.Ports, desired.Spec.Ports)
		obj.Spec.ExternalTrafficPolicy = desired.Spec.ExternalTrafficPolicy
		obj.OwnerReferences = nil
		return nil
	})
	if err != nil {
		return err
	}
	env.Status.IP = lbAddress(obj)
	return nil
}

// mergeNodePorts keeps allocated node ports so updates do not reshuffle them.
func mergeNodePorts(current, desired []corev1.ServicePort) []corev1.ServicePort {
	alloc := map[string]int32{}
	for _, p := range current {
		alloc[fmt.Sprintf("%s/%d", p.Protocol, p.Port)] = p.NodePort
	}
	out := make([]corev1.ServicePort, len(desired))
	for i, p := range desired {
		p.NodePort = alloc[fmt.Sprintf("%s/%d", p.Protocol, p.Port)]
		out[i] = p
	}
	return out
}

func (r *EnvironmentReconciler) ensureStopped(ctx context.Context, env *model.Environment) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: podName(env)}, pod)
	if apierrors.IsNotFound(err) {
		env.Status.PodName = ""
		env.Status.NodeName = ""
		// A multus address belongs to the pod; loadBalancer addresses persist.
		if env.Status.NetworkMode == model.NetworkModeMultus {
			env.Status.IP = ""
		}
		setPhase(env, model.PhaseStopped, "Stopped", "")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod.DeletionTimestamp.IsZero() {
		if err := r.Client.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	setPhase(env, model.PhaseStopping, "Stopping", "")
	return ctrl.Result{}, nil
}

func (r *EnvironmentReconciler) ensureRunning(ctx context.Context, rs *resolved) (ctrl.Result, error) {
	env := rs.env
	rs.helperImage, rs.helperPullPolicy = r.HelperImage, r.HelperImagePullPolicy
	desired, err := rs.desiredPod()
	if err != nil {
		setPhase(env, model.PhaseFailed, "InvalidPodSpec", err.Error())
		return ctrl.Result{}, nil
	}

	pod := &corev1.Pod{}
	err = r.Client.Get(ctx, client.ObjectKeyFromObject(desired), pod)
	if apierrors.IsNotFound(err) {
		if err := r.Client.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		env.Status.PodName = desired.Name
		setPhase(env, model.PhaseStarting, "PodCreated", "")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !pod.DeletionTimestamp.IsZero() {
		setPhase(env, model.PhaseStarting, "Restarting", "waiting for previous pod to terminate")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if pod.Annotations[AnnotationSpecHash] != desired.Annotations[AnnotationSpecHash] {
		// Spec changed (image, resources, class...). Recreate the pod.
		if err := r.Client.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		r.event(env, pod, corev1.EventTypeNormal, "Restarting", "spec changed, recreating pod")
		setPhase(env, model.PhaseStarting, "Restarting", "spec changed")
		return ctrl.Result{}, nil
	}
	if len(pod.OwnerReferences) > 0 {
		// Pods of 0.10 are owned by the DevEnvironment CRD.
		before := pod.DeepCopy()
		pod.OwnerReferences = withoutDevenvOwners(pod.OwnerReferences)
		if !reflect.DeepEqual(before.OwnerReferences, pod.OwnerReferences) {
			if err := r.Client.Patch(ctx, pod, client.MergeFrom(before)); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	env.Status.PodName = pod.Name
	env.Status.NodeName = pod.Spec.NodeName
	if rs.networkMode() == model.NetworkModeMultus {
		env.Status.IP, env.Status.MAC = multusAddress(pod, rs.multusInterface())
	}
	phase, reason, msg := podPhase(pod)
	if phase == model.PhaseRunning && rs.networkMode() != model.NetworkModeGateway && env.Status.IP == "" {
		msg = "waiting for an external address"
	}
	setPhase(env, phase, reason, msg)
	return ctrl.Result{}, nil
}

// finalize removes the Kubernetes objects of an environment marked for
// deletion, then its record.
func (r *EnvironmentReconciler) finalize(ctx context.Context, env *model.Environment) (ctrl.Result, error) {
	ns := r.Namespace
	objs := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName(env), Namespace: ns}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: serviceName(env), Namespace: ns}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: lbServiceName(env), Namespace: ns}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName(env), Namespace: ns}},
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: pvcName(env), Namespace: ns}},
	}
	for _, o := range objs {
		if err := r.Client.Delete(ctx, o); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	// Wait until the pod is gone so a new environment with the same name
	// does not race with it.
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(objs[0]), &corev1.Pod{}); err == nil {
		if env.Status.Phase != model.PhaseStopping {
			setPhase(env, model.PhaseStopping, "Deleting", "")
			_ = r.DB.UpdateEnvironmentStatus(ctx, env.Name, env.Status)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.DB.DeleteEnvironment(ctx, env.Name); err != nil && !errors.Is(err, store.ErrNotFound) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// podPhase maps pod state to an environment phase.
func podPhase(pod *corev1.Pod) (model.Phase, string, string) {
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return model.PhaseFailed, "PodFailed", pod.Status.Message
	case corev1.PodSucceeded:
		return model.PhaseFailed, "PodExited", "environment process exited"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "CrashLoopBackOff", "CreateContainerConfigError", "CreateContainerError":
				return model.PhaseFailed, w.Reason, w.Message
			}
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return model.PhaseStarting, c.Reason, c.Message
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return model.PhaseRunning, "Ready", ""
		}
	}
	return model.PhaseStarting, "Starting", ""
}

func setPhase(env *model.Environment, phase model.Phase, reason, msg string) {
	env.Status.Phase = phase
	env.Status.Reason = reason
	env.Status.Message = msg
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

// SetupWithManager registers the controller. It reacts to database changes
// (this process immediately, other writers through polling), to changes of
// labelled Kubernetes objects and resyncs periodically.
func (r *EnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	byLabel := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		if o.GetNamespace() != r.Namespace {
			return nil
		}
		if name := o.GetLabels()[LabelEnvironment]; name != "" {
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
		}
		return nil
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("environment").
		WatchesRawSource(source.Func(r.storeSource)).
		Watches(&corev1.Pod{}, byLabel).
		Watches(&corev1.Service{}, byLabel).
		Watches(&corev1.PersistentVolumeClaim{}, byLabel).
		Watches(&corev1.Secret{}, byLabel).
		Complete(r)
}

// storeSource feeds database changes and periodic resyncs into the queue.
func (r *EnvironmentReconciler) storeSource(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	changes, cancel := r.DB.Subscribe()
	go func() {
		<-ctx.Done()
		cancel()
	}()
	go r.DB.Poll(ctx, 2*time.Second)
	go func() {
		for c := range changes {
			switch c.Kind {
			case store.KindEnvironment:
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: c.Name}})
			case store.KindUser, store.KindImage:
				// Keys or images changed: recheck every environment.
				r.enqueueAll(ctx, q)
			}
		}
	}()
	go func() {
		t := time.NewTicker(resyncInterval)
		defer t.Stop()
		r.enqueueAll(ctx, q)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.enqueueAll(ctx, q)
			}
		}
	}()
	return nil
}

func (r *EnvironmentReconciler) enqueueAll(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	envs, err := r.DB.ListEnvironments(ctx)
	if err != nil {
		logf.FromContext(ctx).Error(err, "list environments")
		return
	}
	for _, e := range envs {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: e.Name}})
	}
}

// Orphans lists Kubernetes objects of environments that have no database
// record (the database was restored to an earlier point, or objects of a
// deleted installation). They are never deleted automatically.
func Orphans(ctx context.Context, c client.Reader, db *store.Store, namespace string) (map[string][]string, error) {
	envs, err := db.ListEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, e := range envs {
		known[e.Name] = true
	}
	out := map[string][]string{}
	sel := client.MatchingLabels{LabelManagedBy: ManagedByValue}
	add := func(kind string, items []metav1.Object) {
		for _, o := range items {
			name := o.GetLabels()[LabelEnvironment]
			if name != "" && !known[name] {
				out[name] = append(out[name], kind+"/"+o.GetName())
			}
		}
	}
	pvcs := &corev1.PersistentVolumeClaimList{}
	if err := c.List(ctx, pvcs, client.InNamespace(namespace), sel); err != nil {
		return nil, err
	}
	var objs []metav1.Object
	for i := range pvcs.Items {
		objs = append(objs, &pvcs.Items[i])
	}
	add("persistentvolumeclaim", objs)
	pods := &corev1.PodList{}
	if err := c.List(ctx, pods, client.InNamespace(namespace), sel); err != nil {
		return nil, err
	}
	objs = nil
	for i := range pods.Items {
		objs = append(objs, &pods.Items[i])
	}
	add("pod", objs)
	return out, nil
}
