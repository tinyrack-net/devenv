package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete

// PrepullName is the DaemonSet that keeps prepull images on nodes.
const PrepullName = "devenv-image-prepull"

// PrepullReconciler keeps DevImages with prepull: true cached on every node
// that can run environments. It runs a DaemonSet whose pods mount each image
// as a read-only image volume: the kubelet pulls the image, nothing inside it
// runs, and image garbage collection leaves it alone while it is in use. The
// pod's only container is the server image (already on the node) idling.
type PrepullReconciler struct {
	client.Client
	Namespace string
	// Image of the placeholder container (the server image with devenv-agent).
	Image string
}

// +kubebuilder:rbac:groups=devenv.tinyrack.net,resources=devimages;devenvironmentclasses,verbs=get;list;watch

func (r *PrepullReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	images := &devenvv1.DevImageList{}
	if err := r.List(ctx, images); err != nil {
		return ctrl.Result{}, err
	}
	var refs []string
	seen := map[string]bool{}
	for _, img := range images.Items {
		if img.Spec.Prepull && !img.Spec.Disabled && !seen[img.Spec.Image] {
			seen[img.Spec.Image] = true
			refs = append(refs, img.Spec.Image)
		}
	}
	sort.Strings(refs)

	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: PrepullName, Namespace: r.Namespace}}
	if len(refs) == 0 || r.Image == "" {
		if err := r.Delete(ctx, ds); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	sched, err := r.scheduling(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	labels := map[string]string{LabelManagedBy: ManagedByValue, LabelComponent: "image-prepull"}
	var vols []corev1.Volume
	var mounts []corev1.VolumeMount
	for i, ref := range refs {
		name := fmt.Sprintf("image-%d", i)
		vols = append(vols, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{Reference: ref, PullPolicy: corev1.PullIfNotPresent},
		}})
		mounts = append(mounts, corev1.VolumeMount{Name: name, MountPath: "/images/" + name, ReadOnly: true})
	}
	tiny := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1m"), corev1.ResourceMemory: resource.MustParse("8Mi")}

	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, ds, func() error {
		ds.Labels = labels
		ds.Annotations = map[string]string{"devenv.tinyrack.net/images": strings.Join(refs, ",")}
		ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		ds.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
			Type:          appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: ptr.To(intstr.FromString("100%"))},
		}
		ds.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				AutomountServiceAccountToken:  ptr.To(false),
				EnableServiceLinks:            ptr.To(false),
				TerminationGracePeriodSeconds: ptr.To[int64](1),
				NodeSelector:                  sched.nodeSelector,
				Tolerations:                   sched.tolerations,
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot:   ptr.To(true),
					RunAsUser:      ptr.To[int64](65532),
					SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
				},
				Containers: []corev1.Container{{
					Name:            "idle",
					Image:           r.Image,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Command:         []string{"/bin/devenv-agent", "sleep"},
					Resources:       corev1.ResourceRequirements{Requests: tiny, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Mi")}},
					VolumeMounts:    mounts,
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: ptr.To(false),
						ReadOnlyRootFilesystem:   ptr.To(true),
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				}},
				Volumes: vols,
			},
		}
		return nil
	})
	if apierrors.IsConflict(err) {
		return ctrl.Result{Requeue: true}, nil
	}
	if err == nil {
		logf.FromContext(ctx).V(1).Info("image prepull", "images", refs)
	}
	return ctrl.Result{}, err
}

type prepullScheduling struct {
	nodeSelector map[string]string
	tolerations  []corev1.Toleration
}

// scheduling targets nodes environments can use: with a single class its
// node selector; otherwise all nodes. Tolerations of every class are merged.
func (r *PrepullReconciler) scheduling(ctx context.Context) (prepullScheduling, error) {
	classes := &devenvv1.DevEnvironmentClassList{}
	if err := r.List(ctx, classes); err != nil {
		return prepullScheduling{}, err
	}
	out := prepullScheduling{}
	if len(classes.Items) == 1 {
		out.nodeSelector = classes.Items[0].Spec.Scheduling.NodeSelector
	}
	for _, c := range classes.Items {
		out.tolerations = append(out.tolerations, c.Spec.Scheduling.Tolerations...)
	}
	return out, nil
}

func (r *PrepullReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Every relevant change maps to the single DaemonSet.
	one := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: r.Namespace, Name: PrepullName}}}
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("image-prepull").
		Watches(&devenvv1.DevImage{}, one).
		Watches(&devenvv1.DevEnvironmentClass{}, one).
		Watches(&appsv1.DaemonSet{}, one).
		Complete(r)
}
