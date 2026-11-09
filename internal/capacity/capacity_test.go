package capacity

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tinyrack-net/devenv/internal/model"
)

func q(s string) resource.Quantity { return resource.MustParse(s) }
func qp(s string) *resource.Quantity {
	v := q(s)
	return &v
}

func node(name, cpu, mem string, mut ...func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: q(cpu), corev1.ResourceMemory: q(mem)},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	for _, m := range mut {
		m(n)
	}
	return n
}

func pod(ns, name, nodeName, cpu, mem string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{{Name: "c", Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: q(cpu), corev1.ResourceMemory: q(mem)},
			}}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func checker(ns string, objs ...client.Object) *Checker {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	return &Checker{Reader: fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build(), Namespace: ns}
}

func TestReport(t *testing.T) {
	c := checker("",
		node("a", "8", "16Gi"),
		node("b", "4", "32Gi"),
		node("cordoned", "64", "256Gi", func(n *corev1.Node) { n.Spec.Unschedulable = true }),
		node("tainted", "64", "256Gi", func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}}
		}),
		pod("x", "p1", "a", "6", "4Gi"),
		pod("x", "p2", "b", "1", "30Gi"),
		func() *corev1.Pod {
			p := pod("x", "done", "a", "2", "2Gi")
			p.Status.Phase = corev1.PodSucceeded
			return p
		}(),
	)
	rep, err := c.Report(context.Background(), &model.ClassSpec{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Nodes) != 2 {
		t.Fatalf("eligible nodes = %d, want 2", len(rep.Nodes))
	}
	// a: free 2 cpu / 12Gi, b: free 3 cpu / 2Gi
	if rep.Max.CPU.Cmp(q("3")) != 0 || rep.Max.Memory.Cmp(q("12Gi")) != 0 {
		t.Errorf("max = %s/%s", rep.Max.CPU.String(), rep.Max.Memory.String())
	}
	if err := rep.Fits(q("2"), q("8Gi")); err != nil {
		t.Errorf("2/8Gi should fit on a: %v", err)
	}
	// 3 cpu fits only b, 8Gi fits only a: no single node.
	if err := rep.Fits(q("3"), q("8Gi")); err == nil {
		t.Errorf("3/8Gi should not fit on any single node")
	}

	// Excluding p1 frees node a.
	rep, _ = c.Report(context.Background(), &model.ClassSpec{}, &Exclude{Namespace: "x", Name: "p1"})
	if err := rep.Fits(q("8"), q("16Gi")); err != nil {
		t.Errorf("excluded pod should be given back: %v", err)
	}
}

func TestClassSelectorsAndLimits(t *testing.T) {
	c := checker("",
		node("a", "8", "16Gi"),
		node("gpu", "32", "128Gi", func(n *corev1.Node) {
			n.Labels["pool"] = "gpu"
			n.Spec.Taints = []corev1.Taint{{Key: "gpu", Effect: corev1.TaintEffectNoSchedule}}
		}),
	)
	class := &model.ClassSpec{
		Scheduling: model.SchedulingSpec{
			NodeSelector: map[string]string{"pool": "gpu"},
			Tolerations:  []corev1.Toleration{{Key: "gpu", Operator: corev1.TolerationOpExists}},
		},
		Limits: model.Limits{MaxCPU: qp("16"), MaxDisk: qp("100Gi")},
	}
	rep, err := c.Report(context.Background(), class, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Nodes) != 1 || rep.Nodes[0].Name != "gpu" {
		t.Fatalf("nodes = %+v", rep.Nodes)
	}
	if rep.Max.CPU.Cmp(q("16")) != 0 {
		t.Errorf("max cpu should be capped by class: %s", rep.Max.CPU.String())
	}
	if rep.MaxDisk == nil || rep.MaxDisk.Cmp(q("100Gi")) != 0 {
		t.Errorf("max disk = %v", rep.MaxDisk)
	}
	if err := rep.Fits(q("20"), q("1Gi")); err == nil || !strings.Contains(err.Error(), "insufficient") {
		t.Errorf("over class limit should not fit: %v", err)
	}
}

func TestQuota(t *testing.T) {
	quota := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Namespace: "devenv", Name: "q"},
		Status: corev1.ResourceQuotaStatus{
			Hard: corev1.ResourceList{corev1.ResourceRequestsCPU: q("4"), corev1.ResourceLimitsMemory: q("8Gi")},
			Used: corev1.ResourceList{corev1.ResourceRequestsCPU: q("3")},
		},
	}
	c := checker("devenv", node("a", "32", "64Gi"), quota, pod("devenv", "me", "a", "2", "1Gi"))
	rep, err := c.Report(context.Background(), &model.ClassSpec{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Max.CPU.Cmp(q("1")) != 0 {
		t.Errorf("quota should cap cpu at 1, got %s", rep.Max.CPU.String())
	}
	rep, _ = c.Report(context.Background(), &model.ClassSpec{}, &Exclude{Namespace: "devenv", Name: "me"})
	if rep.Max.CPU.Cmp(q("3")) != 0 {
		t.Errorf("excluded pod should return quota, got %s", rep.Max.CPU.String())
	}
}

func TestNoNodes(t *testing.T) {
	rep, _ := checker("").Report(context.Background(), &model.ClassSpec{}, nil)
	if err := rep.Fits(q("1"), q("1Gi")); err == nil {
		t.Errorf("expected no node error")
	}
}
