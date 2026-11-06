package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

const (
	LabelEnvironment = "devenv.tinyrack.net/environment"
	LabelComponent   = "devenv.tinyrack.net/component"
	LabelManagedBy   = "app.kubernetes.io/managed-by"
	ManagedByValue   = "devenv"

	AnnotationSpecHash = "devenv.tinyrack.net/spec-hash"

	ContainerName = "devenv"
	HomeMountPath = "/home/dev"
	SSHMountPath  = "/etc/devenv/ssh"
	SSHPort       = 22
	VNCPort       = 5901

	// Keys of the per-environment SSH Secret.
	AuthorizedKeysFile = "authorized_keys"
	HostKeyFile        = "ssh_host_ed25519_key"
	HostPublicKeyFile  = "ssh_host_ed25519_key.pub"
)

// resolved holds everything needed to render child objects.
type resolved struct {
	env   *devenvv1.DevEnvironment
	image *devenvv1.DevImage
	class *devenvv1.DevEnvironmentClass
}

func podName(env *devenvv1.DevEnvironment) string     { return env.Name }
func serviceName(env *devenvv1.DevEnvironment) string { return env.Name }
func pvcName(env *devenvv1.DevEnvironment) string     { return env.Name + "-home" }
func secretName(env *devenvv1.DevEnvironment) string  { return env.Name + "-ssh" }

func labels(env *devenvv1.DevEnvironment, component string) map[string]string {
	return map[string]string{
		LabelEnvironment: env.Name,
		LabelComponent:   component,
		LabelManagedBy:   ManagedByValue,
	}
}

func selectorLabels(env *devenvv1.DevEnvironment) map[string]string {
	return map[string]string{
		LabelEnvironment: env.Name,
		LabelComponent:   "workspace",
	}
}

func (r *resolved) storageMode() devenvv1.StorageMode {
	return r.class.Spec.EffectiveStorageMode()
}

func (r *resolved) networkMode() devenvv1.NetworkMode {
	return r.class.Spec.EffectiveNetworkMode()
}

func (r *resolved) resources() devenvv1.EffectiveResources {
	return devenvv1.Resolve(r.env.Spec.Resources, &r.image.Spec, &r.class.Spec)
}

func (r *resolved) cpu() resource.Quantity    { return r.resources().CPU }
func (r *resolved) memory() resource.Quantity { return r.resources().Memory }
func (r *resolved) disk() resource.Quantity   { return r.resources().Disk }

// validate checks the request against class limits.
func (r *resolved) validate() error {
	return r.class.Spec.CheckLimits(r.resources())
}

// desiredSecret renders authorized_keys. Host keys are added by the reconciler
// so they stay stable across reconciles.
func (r *resolved) desiredSecret(extraKeys ...string) *corev1.Secret {
	keys := make([]string, 0, len(r.env.Spec.SSHAuthorizedKeys)+len(extraKeys))
	for _, k := range append(append([]string{}, r.env.Spec.SSHAuthorizedKeys...), extraKeys...) {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	content := strings.Join(keys, "\n")
	if content != "" {
		content += "\n"
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName(r.env),
			Namespace: r.env.Namespace,
			Labels:    labels(r.env, "ssh"),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{AuthorizedKeysFile: []byte(content)},
	}
}

func (r *resolved) desiredPVC() *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(r.env),
			Namespace: r.env.Namespace,
			Labels:    labels(r.env, "home"),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: r.class.Spec.Storage.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: r.disk()},
			},
		},
	}
}

func (r *resolved) desiredService() *corev1.Service {
	ports := []corev1.ServicePort{{
		Name:       "ssh",
		Port:       SSHPort,
		TargetPort: intstr.FromInt32(SSHPort),
		Protocol:   corev1.ProtocolTCP,
	}}
	// VNC is reached through an SSH tunnel and listens on localhost only.
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName(r.env),
			Namespace: r.env.Namespace,
			Labels:    labels(r.env, "service"),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selectorLabels(r.env),
			Ports:    ports,
		},
	}
}

func (r *resolved) desiredPod() (*corev1.Pod, error) {
	env := r.env
	res := corev1.ResourceList{
		corev1.ResourceCPU:    r.cpu(),
		corev1.ResourceMemory: r.memory(),
	}

	var homeVolume corev1.VolumeSource
	if r.storageMode() == devenvv1.StorageModePersistent {
		homeVolume = corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName(env)}}
	} else {
		homeVolume = corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
	}

	ports := []corev1.ContainerPort{{Name: "ssh", ContainerPort: SSHPort, Protocol: corev1.ProtocolTCP}}

	podLabels := labels(env, "workspace")
	sched := r.class.Spec.Scheduling

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName(env),
			Namespace: env.Namespace,
			Labels:    podLabels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy:                 corev1.RestartPolicyAlways,
			AutomountServiceAccountToken:  ptr.To(false),
			EnableServiceLinks:            ptr.To(false),
			TerminationGracePeriodSeconds: ptr.To[int64](30),
			NodeSelector:                  sched.NodeSelector,
			Tolerations:                   sched.Tolerations,
			Affinity:                      sched.Affinity,
			RuntimeClassName:              sched.RuntimeClassName,
			PriorityClassName:             sched.PriorityClassName,
			Containers: []corev1.Container{{
				Name:            ContainerName,
				Image:           r.image.Spec.Image,
				ImagePullPolicy: r.image.Spec.ImagePullPolicy,
				Ports:           ports,
				Env: []corev1.EnvVar{
					{Name: "DEVENV_NAME", Value: env.Name},
					{Name: "DEVENV_OWNER", Value: env.Spec.Owner},
					{Name: "DEVENV_DESKTOP", Value: strconv.FormatBool(r.image.Spec.Desktop)},
				},
				Resources: corev1.ResourceRequirements{Requests: res, Limits: res},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "home", MountPath: HomeMountPath},
					{Name: "ssh", MountPath: SSHMountPath, ReadOnly: true},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(SSHPort)},
					},
					PeriodSeconds: 5,
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "home", VolumeSource: homeVolume},
				{Name: "ssh", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
					SecretName:  secretName(env),
					DefaultMode: ptr.To[int32](0o644),
					Items: []corev1.KeyToPath{
						{Key: AuthorizedKeysFile, Path: AuthorizedKeysFile},
						{Key: HostKeyFile, Path: HostKeyFile, Mode: ptr.To[int32](0o600)},
						{Key: HostPublicKeyFile, Path: HostPublicKeyFile},
					},
				}}},
			},
		},
	}
	if errs := validation.IsDNS1123Label(env.Name); len(errs) == 0 {
		pod.Spec.Hostname = env.Name
	}

	if err := applyPodOverrides(pod, r.class); err != nil {
		return nil, err
	}
	// Ensure identity labels survive overrides.
	for k, v := range podLabels {
		pod.Labels[k] = v
	}

	hash, err := hashPod(pod)
	if err != nil {
		return nil, err
	}
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[AnnotationSpecHash] = hash
	return pod, nil
}

// hashPod hashes the parts of the pod the controller owns. Pods are mostly
// immutable, so a changed hash means the pod must be recreated.
func hashPod(pod *corev1.Pod) (string, error) {
	b, err := json.Marshal(struct {
		Labels      map[string]string
		Annotations map[string]string
		Spec        corev1.PodSpec
	}{pod.Labels, pod.Annotations, pod.Spec})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8]), nil
}

func applyPodOverrides(pod *corev1.Pod, class *devenvv1.DevEnvironmentClass) error {
	if class.Spec.PodOverrides == nil || len(class.Spec.PodOverrides.Raw) == 0 {
		return nil
	}
	orig, err := json.Marshal(pod)
	if err != nil {
		return err
	}
	patched, err := strategicpatch.StrategicMergePatch(orig, class.Spec.PodOverrides.Raw, corev1.Pod{})
	if err != nil {
		return fmt.Errorf("apply podOverrides: %w", err)
	}
	out := &corev1.Pod{}
	if err := json.Unmarshal(patched, out); err != nil {
		return fmt.Errorf("apply podOverrides: %w", err)
	}
	*pod = *out
	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	return nil
}
