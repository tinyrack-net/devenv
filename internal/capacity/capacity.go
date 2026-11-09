// Package capacity estimates whether an environment fits on the cluster.
//
// The estimate is intentionally conservative but simple: it considers node
// readiness, cordoning, the class nodeSelector and taints/tolerations, and
// compares requested CPU/memory with allocatable minus the requests of all
// non-terminated pods. Affinity rules and topology spread are not evaluated,
// so the scheduler can still reject a request; the controller surfaces that
// through the environment status.
package capacity

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/model"
)

// NodeCapacity is the free capacity of a single node.
type NodeCapacity struct {
	Name      string            `json:"name"`
	Allocated ResourcePair      `json:"allocated"`
	Allocable ResourcePair      `json:"allocatable"`
	Free      ResourcePair      `json:"free"`
	Labels    map[string]string `json:"-"`
}

// ResourcePair holds CPU and memory quantities.
type ResourcePair struct {
	CPU    resource.Quantity `json:"cpu"`
	Memory resource.Quantity `json:"memory"`
}

// Report summarizes what can be requested for a class.
type Report struct {
	Nodes []NodeCapacity `json:"nodes"`
	// Max is the largest CPU and memory available on any single eligible node,
	// capped by class limits and namespace quotas.
	Max ResourcePair `json:"max"`
	// MaxDisk is the class disk limit, if any.
	MaxDisk *resource.Quantity `json:"maxDisk,omitempty"`
}

// Checker computes capacity using an (ideally uncached) reader.
type Checker struct {
	Reader    client.Reader
	Namespace string
}

// Exclude identifies a pod whose requests must not be counted, e.g. the
// current pod of an environment being resized.
type Exclude = types.NamespacedName

// Report computes capacity for the class.
func (c *Checker) Report(ctx context.Context, class *model.ClassSpec, exclude *Exclude) (*Report, error) {
	nodes := &corev1.NodeList{}
	if err := c.Reader.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	pods := &corev1.PodList{}
	if err := c.Reader.List(ctx, pods); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	used := map[string]corev1.ResourceList{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Spec.NodeName == "" || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if exclude != nil && p.Namespace == exclude.Namespace && p.Name == exclude.Name {
			continue
		}
		addTo(used, p.Spec.NodeName, PodRequests(p))
	}

	rep := &Report{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if !Eligible(n, class) {
			continue
		}
		alloc := n.Status.Allocatable
		u := used[n.Name]
		nc := NodeCapacity{
			Name:      n.Name,
			Allocable: ResourcePair{CPU: alloc.Cpu().DeepCopy(), Memory: alloc.Memory().DeepCopy()},
			Allocated: ResourcePair{CPU: u.Cpu().DeepCopy(), Memory: u.Memory().DeepCopy()},
		}
		nc.Free = ResourcePair{CPU: sub(nc.Allocable.CPU, nc.Allocated.CPU), Memory: sub(nc.Allocable.Memory, nc.Allocated.Memory)}
		rep.Nodes = append(rep.Nodes, nc)
		if nc.Free.CPU.Cmp(rep.Max.CPU) > 0 {
			rep.Max.CPU = nc.Free.CPU.DeepCopy()
		}
		if nc.Free.Memory.Cmp(rep.Max.Memory) > 0 {
			rep.Max.Memory = nc.Free.Memory.DeepCopy()
		}
	}

	capTo(&rep.Max.CPU, class.Limits.MaxCPU)
	capTo(&rep.Max.Memory, class.Limits.MaxMemory)
	if class.EffectiveStorageMode() == model.StorageModePersistent && class.Limits.MaxDisk != nil {
		d := class.Limits.MaxDisk.DeepCopy()
		rep.MaxDisk = &d
	}

	if c.Namespace != "" {
		q, err := c.quotaFree(ctx, exclude)
		if err != nil {
			return nil, err
		}
		if v, ok := q[corev1.ResourceRequestsCPU]; ok {
			capTo(&rep.Max.CPU, &v)
		}
		if v, ok := q[corev1.ResourceRequestsMemory]; ok {
			capTo(&rep.Max.Memory, &v)
		}
		if v, ok := q[corev1.ResourceRequestsStorage]; ok && rep.MaxDisk != nil {
			capTo(rep.MaxDisk, &v)
		}
	}
	return rep, nil
}

// Fits reports whether cpu/memory fit on at least one eligible node.
func (r *Report) Fits(cpu, memory resource.Quantity) error {
	if len(r.Nodes) == 0 {
		return fmt.Errorf("no schedulable node matches the class")
	}
	// Max already includes class limits and quotas.
	if cpu.Cmp(r.Max.CPU) <= 0 && memory.Cmp(r.Max.Memory) <= 0 {
		for _, n := range r.Nodes {
			if cpu.Cmp(n.Free.CPU) <= 0 && memory.Cmp(n.Free.Memory) <= 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("insufficient capacity: requested cpu %s / memory %s, available at most cpu %s / memory %s",
		cpu.String(), memory.String(), r.Max.CPU.String(), r.Max.Memory.String())
}

// quotaFree returns hard-used for every ResourceQuota in the namespace (minimum across quotas).
func (c *Checker) quotaFree(ctx context.Context, exclude *Exclude) (corev1.ResourceList, error) {
	quotas := &corev1.ResourceQuotaList{}
	if err := c.Reader.List(ctx, quotas, client.InNamespace(c.Namespace)); err != nil {
		return nil, fmt.Errorf("list resourcequotas: %w", err)
	}
	var own corev1.ResourceList
	if exclude != nil {
		p := &corev1.Pod{}
		if err := c.Reader.Get(ctx, *exclude, p); err == nil {
			own = PodRequests(p)
		}
	}
	free := corev1.ResourceList{}
	for _, q := range quotas.Items {
		for name, hard := range q.Status.Hard {
			used := q.Status.Used[name]
			f := sub(hard, used)
			// Give back what the excluded pod currently uses.
			switch name {
			case corev1.ResourceRequestsCPU, corev1.ResourceCPU:
				f.Add(own[corev1.ResourceCPU])
				name = corev1.ResourceRequestsCPU
			case corev1.ResourceRequestsMemory, corev1.ResourceMemory:
				f.Add(own[corev1.ResourceMemory])
				name = corev1.ResourceRequestsMemory
			}
			if cur, ok := free[name]; !ok || f.Cmp(cur) < 0 {
				free[name] = f
			}
		}
	}
	return free, nil
}

// Eligible reports whether a node can host environments of the class.
func Eligible(n *corev1.Node, class *model.ClassSpec) bool {
	if n.Spec.Unschedulable {
		return false
	}
	ready := false
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return false
	}
	for k, v := range class.Scheduling.NodeSelector {
		if n.Labels[k] != v {
			return false
		}
	}
	for i := range n.Spec.Taints {
		t := &n.Spec.Taints[i]
		if t.Effect == corev1.TaintEffectPreferNoSchedule {
			continue
		}
		tolerated := false
		for j := range class.Scheduling.Tolerations {
			if class.Scheduling.Tolerations[j].ToleratesTaint(klog.Background(), t, true) {
				tolerated = true
				break
			}
		}
		if !tolerated {
			return false
		}
	}
	return true
}

// PodRequests returns the effective CPU/memory requests of a pod:
// max(sum(containers), max(initContainers)) + overhead.
func PodRequests(p *corev1.Pod) corev1.ResourceList {
	out := corev1.ResourceList{}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		var sum resource.Quantity
		for _, c := range p.Spec.Containers {
			if q, ok := c.Resources.Requests[name]; ok {
				sum.Add(q)
			}
		}
		for _, c := range p.Spec.InitContainers {
			if q, ok := c.Resources.Requests[name]; ok && q.Cmp(sum) > 0 {
				sum = q.DeepCopy()
			}
		}
		if q, ok := p.Spec.Overhead[name]; ok {
			sum.Add(q)
		}
		out[name] = sum
	}
	return out
}

func addTo(m map[string]corev1.ResourceList, node string, r corev1.ResourceList) {
	cur, ok := m[node]
	if !ok {
		cur = corev1.ResourceList{}
		m[node] = cur
	}
	for k, v := range r {
		q := cur[k]
		q.Add(v)
		cur[k] = q
	}
}

func sub(a, b resource.Quantity) resource.Quantity {
	out := a.DeepCopy()
	out.Sub(b)
	if out.Sign() < 0 {
		return resource.Quantity{Format: a.Format}
	}
	return out
}

func capTo(q *resource.Quantity, max *resource.Quantity) {
	if max != nil && q.Cmp(*max) > 0 {
		*q = max.DeepCopy()
	}
}
