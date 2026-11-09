package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

const (
	// Multus annotations.
	annotationNetworks      = "k8s.v1.cni.cncf.io/networks"
	annotationNetworkStatus = "k8s.v1.cni.cncf.io/network-status"

	defaultMultusInterface = "net1"
)

// AnnotationAwaitVolume makes the controller wait for an existing home
// volume (restored from a backup) instead of creating an empty one.
const AnnotationAwaitVolume = "devenv.tinyrack.net/await-volume"

// AnnotationStableMAC pins the multus MAC of an environment. Backups record
// it so restores keep the address even for environments created before the
// MAC was derived from the name.
const AnnotationStableMAC = "devenv.tinyrack.net/stable-mac"


func lbServiceName(env *devenvv1.DevEnvironment) string { return env.Name + "-lb" }

// stableMAC derives a locally administered unicast MAC from the environment
// UID, so DHCP servers hand out the same lease after restarts.
// stableMAC returns the MAC of a multus environment. Environments that
// already have one keep it; new ones derive it from namespace/name (not the
// UID), so recreating the object from a backup keeps the DHCP lease.
func stableMAC(env *devenvv1.DevEnvironment) string {
	if m := env.Status.StableMAC; m != "" {
		return m
	}
	if m := env.Annotations[AnnotationStableMAC]; m != "" {
		return m
	}
	sum := sha256.Sum256([]byte(env.Namespace + "/" + env.Name))
	mac := net.HardwareAddr(sum[:6])
	mac[0] = (mac[0] | 0x02) & 0xfe // locally administered, unicast
	return mac.String()
}

// multusSelection is one entry of the k8s.v1.cni.cncf.io/networks annotation.
type multusSelection struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Interface string `json:"interface,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

func (r *resolved) multusInterface() string {
	if m := r.class.Spec.Network.Multus; m != nil && m.Interface != "" {
		return m.Interface
	}
	return defaultMultusInterface
}

// multusAnnotation renders the network selection for the environment pod.
func (r *resolved) multusAnnotation() (string, error) {
	m := r.class.Spec.Network.Multus
	if m == nil || m.NetworkAttachment == "" {
		return "", fmt.Errorf("class %q has no multus.networkAttachment", r.class.Name)
	}
	sel := multusSelection{Name: m.NetworkAttachment, Interface: r.multusInterface()}
	if ns, name, ok := strings.Cut(m.NetworkAttachment, "/"); ok {
		sel.Namespace, sel.Name = ns, name
	}
	if m.StableMAC {
		sel.MAC = stableMAC(r.env)
	}
	b, err := json.Marshal([]multusSelection{sel})
	return string(b), err
}

// networkStatus is one entry of the k8s.v1.cni.cncf.io/network-status annotation.
type networkStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	IPs       []string `json:"ips"`
	MAC       string   `json:"mac"`
	Default   bool     `json:"default"`
}

// multusAddress returns the IP and MAC of the secondary interface.
func multusAddress(pod *corev1.Pod, iface string) (ip, mac string) {
	raw := pod.Annotations[annotationNetworkStatus]
	if raw == "" {
		return "", ""
	}
	var st []networkStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return "", ""
	}
	for _, s := range st {
		if s.Default || s.Interface != iface {
			continue
		}
		// Prefer IPv4.
		for _, a := range s.IPs {
			if p := net.ParseIP(a); p != nil && p.To4() != nil {
				return a, s.MAC
			}
		}
		if len(s.IPs) > 0 {
			return s.IPs[0], s.MAC
		}
		return "", s.MAC
	}
	return "", ""
}

// desiredLBService renders the per-environment LoadBalancer Service.
func (r *resolved) desiredLBService() *corev1.Service {
	env := r.env
	ports := []corev1.ServicePort{{
		Name: "ssh", Port: SSHPort, TargetPort: intstr.FromInt32(SSHPort), Protocol: corev1.ProtocolTCP,
	}}
	for _, p := range env.Spec.Ports {
		if p.Port == SSHPort && p.Protocol != "UDP" {
			continue
		}
		proto := corev1.ProtocolTCP
		if p.Protocol == "UDP" {
			proto = corev1.ProtocolUDP
		}
		ports = append(ports, corev1.ServicePort{
			Name:       fmt.Sprintf("%s-%d", strings.ToLower(string(proto)), p.Port),
			Port:       p.Port,
			TargetPort: intstr.FromInt32(p.Port),
			Protocol:   proto,
		})
	}

	svcLabels := labels(env, "loadbalancer")
	var annotations map[string]string
	var lbClass *string
	if lb := r.class.Spec.Network.LoadBalancer; lb != nil {
		for k, v := range lb.Labels {
			svcLabels[k] = v
		}
		annotations = lb.Annotations
		lbClass = lb.LoadBalancerClassName
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        lbServiceName(env),
			Namespace:   env.Namespace,
			Labels:      svcLabels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:                  corev1.ServiceTypeLoadBalancer,
			LoadBalancerClass:     lbClass,
			Selector:              selectorLabels(env),
			Ports:                 ports,
			ExternalTrafficPolicy: corev1.ServiceExternalTrafficPolicyLocal,
		},
	}
}

// lbAddress returns the first ingress address of a LoadBalancer Service.
func lbAddress(svc *corev1.Service) string {
	for _, in := range svc.Status.LoadBalancer.Ingress {
		if in.IP != "" {
			return in.IP
		}
		if in.Hostname != "" {
			return in.Hostname
		}
	}
	return ""
}
