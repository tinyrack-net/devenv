package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

const (
	indexImageRef  = "spec.imageRef"
	indexClassName = "spec.className"
	indexPassword  = "spec.passwordSecretName"
)

// DevEnvironmentReconciler reconciles DevEnvironment objects.
type DevEnvironmentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// GatewayAuthorizedKey is the public key the devenv server uses to connect
	// to environments (SSH gateway, web terminal, VNC tunnel). Optional.
	GatewayAuthorizedKey string
}

// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devenvironments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devenvironments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devimages;devenvironmentclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods;services;secrets;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

func (r *DevEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	env := &devenvv1.DevEnvironment{}
	if err := r.Get(ctx, req.NamespacedName, env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !env.DeletionTimestamp.IsZero() {
		// Child objects are garbage collected through owner references.
		return ctrl.Result{}, nil
	}

	orig := env.DeepCopy()
	res, err := r.reconcile(ctx, env)

	env.Status.ObservedGeneration = env.Generation
	if !equalStatus(orig.Status, env.Status) {
		if uerr := r.Status().Update(ctx, env); uerr != nil {
			if apierrors.IsConflict(uerr) {
				return ctrl.Result{Requeue: true}, nil
			}
			log.Error(uerr, "update status")
			if err == nil {
				err = uerr
			}
		}
	}
	return res, err
}

func (r *DevEnvironmentReconciler) reconcile(ctx context.Context, env *devenvv1.DevEnvironment) (ctrl.Result, error) {
	rs, msg, err := r.resolve(ctx, env)
	if err != nil {
		return ctrl.Result{}, err
	}
	if rs == nil {
		setPhase(env, devenvv1.PhasePending, "Unresolved", msg)
		return ctrl.Result{}, nil
	}
	env.Status.Image = rs.image.Spec.Image
	env.Status.Desktop = rs.image.Spec.Desktop

	if err := rs.validate(); err != nil {
		setPhase(env, devenvv1.PhaseFailed, "InvalidSpec", err.Error())
		return ctrl.Result{}, nil
	}
	env.Status.NetworkMode = rs.networkMode()

	if err := r.ensureSecret(ctx, rs); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure secret: %w", err)
	}
	if rs.storageMode() == devenvv1.StorageModePersistent {
		if err := r.ensurePVC(ctx, rs); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure pvc: %w", err)
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
// means a dependency is missing; the environment is re-queued by watches.
func (r *DevEnvironmentReconciler) resolve(ctx context.Context, env *devenvv1.DevEnvironment) (*resolved, string, error) {
	image := &devenvv1.DevImage{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.Spec.ImageRef}, image); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("DevImage %q not found", env.Spec.ImageRef), nil
		}
		return nil, "", err
	}
	class := &devenvv1.DevEnvironmentClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: env.ClassName()}, class); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("DevEnvironmentClass %q not found", env.ClassName()), nil
		}
		return nil, "", err
	}
	return &resolved{env: env, image: image, class: class}, "", nil
}

func (r *DevEnvironmentReconciler) ensureSecret(ctx context.Context, rs *resolved) error {
	var extra []string
	if r.GatewayAuthorizedKey != "" {
		extra = append(extra, r.GatewayAuthorizedKey)
	}
	desired := rs.desiredSecret(extra...)
	obj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	current := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(obj), current); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		current = nil
	}
	pwHash, pwSource, pwErr := r.resolvePassword(ctx, rs.env, current)
	if pwErr != nil {
		// Keep the environment usable with keys; report the problem.
		r.Recorder.Eventf(rs.env, nil, corev1.EventTypeWarning, "PasswordInvalid", "Password", "%v", pwErr)
		pwHash, pwSource = "", ""
	}
	rs.env.Status.PasswordSet = pwHash != ""
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		priv, pub := obj.Data[HostKeyFile], obj.Data[HostPublicKeyFile]
		if len(priv) == 0 || len(pub) == 0 {
			var err error
			if priv, pub, err = generateHostKey(); err != nil {
				return err
			}
		}
		obj.Labels = desired.Labels
		obj.Type = desired.Type
		obj.Data = desired.Data
		obj.Data[HostKeyFile] = priv
		obj.Data[HostPublicKeyFile] = pub
		obj.Data[PasswordHashFile] = []byte(pwHash)
		if obj.Annotations == nil {
			obj.Annotations = map[string]string{}
		}
		if pwSource != "" {
			obj.Annotations[annotationPasswordSource] = pwSource
		} else {
			delete(obj.Annotations, annotationPasswordSource)
		}
		return controllerutil.SetControllerReference(rs.env, obj, r.Scheme)
	})
	return err
}

func (r *DevEnvironmentReconciler) ensurePVC(ctx context.Context, rs *resolved) error {
	desired := rs.desiredPVC()
	obj := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), obj)
	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(rs.env, desired, r.Scheme); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Only growth is supported (enforced by CRD validation and Kubernetes).
	want := desired.Spec.Resources.Requests[corev1.ResourceStorage]
	have := obj.Spec.Resources.Requests[corev1.ResourceStorage]
	if want.Cmp(have) > 0 {
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.Resources.Requests[corev1.ResourceStorage] = want
		if err := r.Patch(ctx, obj, patch); err != nil {
			logf.FromContext(ctx).Info("resize home volume failed", "error", err.Error())
			r.Recorder.Eventf(rs.env, obj, corev1.EventTypeWarning, "ResizeFailed", "Resize", "resize home volume: %v", err)
			return nil
		}
	}
	return nil
}

func (r *DevEnvironmentReconciler) ensureService(ctx context.Context, rs *resolved) error {
	desired := rs.desiredService()
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: desired.Name, Namespace: desired.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		obj.Labels = desired.Labels
		obj.Spec.Type = desired.Spec.Type
		obj.Spec.Selector = desired.Spec.Selector
		obj.Spec.Ports = desired.Spec.Ports
		return controllerutil.SetControllerReference(rs.env, obj, r.Scheme)
	})
	return err
}

// ensureLBService manages the LoadBalancer Service in loadBalancer mode and
// removes it otherwise. The Service survives stop so the address is kept.
func (r *DevEnvironmentReconciler) ensureLBService(ctx context.Context, rs *resolved) error {
	env := rs.env
	if rs.networkMode() != devenvv1.NetworkModeLoadBalancer {
		svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: lbServiceName(env), Namespace: env.Namespace}}
		if err := r.Delete(ctx, svc); client.IgnoreNotFound(err) != nil {
			return err
		}
		if rs.networkMode() == devenvv1.NetworkModeGateway {
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
		return controllerutil.SetControllerReference(env, obj, r.Scheme)
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

func (r *DevEnvironmentReconciler) ensureStopped(ctx context.Context, env *devenvv1.DevEnvironment) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Namespace: env.Namespace, Name: podName(env)}, pod)
	if apierrors.IsNotFound(err) {
		env.Status.PodName = ""
		env.Status.NodeName = ""
		// A multus address belongs to the pod; loadBalancer addresses persist.
		if env.Status.NetworkMode == devenvv1.NetworkModeMultus {
			env.Status.IP = ""
		}
		setPhase(env, devenvv1.PhaseStopped, "Stopped", "")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if pod.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
	}
	setPhase(env, devenvv1.PhaseStopping, "Stopping", "")
	return ctrl.Result{}, nil
}

func (r *DevEnvironmentReconciler) ensureRunning(ctx context.Context, rs *resolved) (ctrl.Result, error) {
	env := rs.env
	desired, err := rs.desiredPod()
	if err != nil {
		setPhase(env, devenvv1.PhaseFailed, "InvalidPodSpec", err.Error())
		return ctrl.Result{}, nil
	}

	pod := &corev1.Pod{}
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), pod)
	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(env, desired, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		env.Status.PodName = desired.Name
		setPhase(env, devenvv1.PhaseStarting, "PodCreated", "")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !pod.DeletionTimestamp.IsZero() {
		setPhase(env, devenvv1.PhaseStarting, "Restarting", "waiting for previous pod to terminate")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if pod.Annotations[AnnotationSpecHash] != desired.Annotations[AnnotationSpecHash] {
		// Spec changed (image, resources, class...). Recreate the pod.
		if err := r.Delete(ctx, pod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(env, pod, corev1.EventTypeNormal, "Restarting", "Recreate", "spec changed, recreating pod")
		setPhase(env, devenvv1.PhaseStarting, "Restarting", "spec changed")
		return ctrl.Result{}, nil
	}

	env.Status.PodName = pod.Name
	env.Status.NodeName = pod.Spec.NodeName
	if rs.networkMode() == devenvv1.NetworkModeMultus {
		env.Status.IP, env.Status.MAC = multusAddress(pod, rs.multusInterface())
	}
	phase, reason, msg := podPhase(pod)
	if phase == devenvv1.PhaseRunning && rs.networkMode() != devenvv1.NetworkModeGateway && env.Status.IP == "" {
		msg = "waiting for an external address"
	}
	setPhase(env, phase, reason, msg)
	return ctrl.Result{}, nil
}

// podPhase maps pod state to an environment phase.
func podPhase(pod *corev1.Pod) (devenvv1.Phase, string, string) {
	switch pod.Status.Phase {
	case corev1.PodFailed:
		return devenvv1.PhaseFailed, "PodFailed", pod.Status.Message
	case corev1.PodSucceeded:
		return devenvv1.PhaseFailed, "PodExited", "environment process exited"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName", "CrashLoopBackOff", "CreateContainerConfigError", "CreateContainerError":
				return devenvv1.PhaseFailed, w.Reason, w.Message
			}
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
			return devenvv1.PhaseStarting, c.Reason, c.Message
		}
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return devenvv1.PhaseRunning, "Ready", ""
		}
	}
	return devenvv1.PhaseStarting, "Starting", ""
}

func setPhase(env *devenvv1.DevEnvironment, phase devenvv1.Phase, reason, msg string) {
	env.Status.Phase = phase
	env.Status.Message = msg
	status := metav1.ConditionFalse
	if phase == devenvv1.PhaseRunning {
		status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&env.Status.Conditions, metav1.Condition{
		Type:               devenvv1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: env.Generation,
	})
}

func equalStatus(a, b devenvv1.DevEnvironmentStatus) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

// SetupWithManager registers the controller and its watches.
func (r *DevEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(ctx, &devenvv1.DevEnvironment{}, indexImageRef, func(o client.Object) []string {
		return []string{o.(*devenvv1.DevEnvironment).Spec.ImageRef}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &devenvv1.DevEnvironment{}, indexClassName, func(o client.Object) []string {
		return []string{o.(*devenvv1.DevEnvironment).ClassName()}
	}); err != nil {
		return err
	}

	if err := idx.IndexField(ctx, &devenvv1.DevEnvironment{}, indexPassword, func(o client.Object) []string {
		if n := o.(*devenvv1.DevEnvironment).Spec.PasswordSecretName; n != "" {
			return []string{n}
		}
		return nil
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&devenvv1.DevEnvironment{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&devenvv1.DevImage{}, handler.EnqueueRequestsFromMapFunc(r.mapBy(indexImageRef))).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mapPasswordSecret)).
		Watches(&devenvv1.DevEnvironmentClass{}, handler.EnqueueRequestsFromMapFunc(r.mapBy(indexClassName))).
		Named("devenvironment").
		Complete(r)
}

// mapPasswordSecret enqueues environments referencing a password Secret.
func (r *DevEnvironmentReconciler) mapPasswordSecret(ctx context.Context, o client.Object) []reconcile.Request {
	list := &devenvv1.DevEnvironmentList{}
	if err := r.List(ctx, list, client.InNamespace(o.GetNamespace()), client.MatchingFields{indexPassword: o.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, e := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&e)})
	}
	return reqs
}

func (r *DevEnvironmentReconciler) mapBy(field string) handler.MapFunc {
	return func(ctx context.Context, o client.Object) []reconcile.Request {
		list := &devenvv1.DevEnvironmentList{}
		if err := r.List(ctx, list, client.MatchingFields{field: o.GetName()}); err != nil {
			logf.FromContext(ctx).Error(err, "list environments", "field", field)
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, e := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&e)})
		}
		return reqs
	}
}
