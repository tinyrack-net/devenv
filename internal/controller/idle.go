package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

// now is replaceable in tests.
var now = time.Now

// lastActive is the latest of the recorded activity, the creation time and
// the time the pod became ready (so a freshly started environment gets a
// full idle period).
func lastActive(env *devenvv1.DevEnvironment, readySince time.Time) time.Time {
	t := env.CreationTimestamp.Time
	if a := env.Status.LastActivityTime; a != nil && a.Time.After(t) {
		t = a.Time
	}
	if readySince.After(t) {
		t = readySince
	}
	return t
}

// checkIdle stops environments that have been idle longer than
// spec.idleTimeout. It returns whether it stopped the environment, and
// otherwise how long until the next check.
func (r *DevEnvironmentReconciler) checkIdle(ctx context.Context, env *devenvv1.DevEnvironment) (bool, time.Duration, error) {
	if env.Spec.IdleTimeout == nil || env.Spec.IdleTimeout.Duration <= 0 || !env.IsRunning() {
		return false, 0, nil
	}
	if env.Status.Phase != devenvv1.PhaseRunning {
		// Only running environments idle out; starting ones get a full period.
		return false, 0, nil
	}
	var readySince time.Time
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: env.Namespace, Name: podName(env)}, pod); err == nil {
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				readySince = c.LastTransitionTime.Time
			}
		}
	}
	timeout := env.Spec.IdleTimeout.Duration
	idle := now().Sub(lastActive(env, readySince))
	if idle < timeout {
		return false, timeout - idle + time.Second, nil
	}

	patch := client.MergeFrom(env.DeepCopy())
	env.Spec.Running = ptr.To(false)
	if err := r.Patch(ctx, env, patch); err != nil {
		return false, 0, client.IgnoreNotFound(err)
	}
	logf.FromContext(ctx).Info("stopped idle environment", "idle", idle.Round(time.Second).String(), "idleTimeout", timeout.String())
	r.Recorder.Eventf(env, nil, corev1.EventTypeNormal, "IdleStopped", "Stop",
		"stopped after %s without activity (idleTimeout %s)", idle.Round(time.Minute), timeout)
	return true, 0, nil
}
