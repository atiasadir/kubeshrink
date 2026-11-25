// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import (
	"strings"
	"testing"
)

func TestParseQuantity(t *testing.T) {
	tests := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"250m", 0.25, false},
		{"2", 2, false},
		{"1.5", 1.5, false},
		{"512Mi", 512 << 20, false},
		{"1Gi", 1 << 30, false},
		{"1G", 1e9, false},
		{"1e3", 1000, false},
		{"", 0, true},
		{"12Qi", 0, true},
		{"abc", 0, true},
		{"-1", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseQuantity(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseQuantity(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseQuantity(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestFormat(t *testing.T) {
	if got := FormatCPU(2500); got != "2500m" {
		t.Errorf("FormatCPU(2500) = %q", got)
	}
	if got := FormatCPU(4000); got != "4" {
		t.Errorf("FormatCPU(4000) = %q", got)
	}
	if got := FormatMemory(1536 << 20); got != "1536Mi" {
		t.Errorf("FormatMemory(1536Mi) = %q", got)
	}
	if got := FormatMemory(-(1 << 30)); got != "-1Gi" {
		t.Errorf("FormatMemory(-1Gi) = %q", got)
	}
	if got := FormatResourceList(map[string]string{"memory": "2Gi", "cpu": "1"}); got != "cpu=1, memory=2Gi" {
		t.Errorf("FormatResourceList = %q", got)
	}
}

func TestLabelSelectorMatches(t *testing.T) {
	labels := map[string]string{"app": "web", "tier": "front"}
	tests := []struct {
		name string
		sel  *LabelSelector
		want bool
	}{
		{"nil selects nothing", nil, false},
		{"empty selects everything", &LabelSelector{}, true},
		{"matchLabels hit", &LabelSelector{MatchLabels: map[string]string{"app": "web"}}, true},
		{"matchLabels miss", &LabelSelector{MatchLabels: map[string]string{"app": "db"}}, false},
		{"In", &LabelSelector{MatchExpressions: []LabelSelectorRequirement{{Key: "tier", Operator: "In", Values: []string{"front", "back"}}}}, true},
		{"NotIn", &LabelSelector{MatchExpressions: []LabelSelectorRequirement{{Key: "tier", Operator: "NotIn", Values: []string{"front"}}}}, false},
		{"Exists", &LabelSelector{MatchExpressions: []LabelSelectorRequirement{{Key: "app", Operator: "Exists"}}}, true},
		{"DoesNotExist", &LabelSelector{MatchExpressions: []LabelSelectorRequirement{{Key: "app", Operator: "DoesNotExist"}}}, false},
		{"unknown operator", &LabelSelector{MatchExpressions: []LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}}, false},
	}
	for _, tt := range tests {
		if got := tt.sel.Matches(labels); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestNodeSelectorMatches(t *testing.T) {
	node := &Node{Metadata: ObjectMeta{Name: "n1", Labels: map[string]string{"zone": "a", "size": "8"}}}
	req := func(key, op string, values ...string) NodeSelectorRequirement {
		return NodeSelectorRequirement{Key: key, Operator: op, Values: values}
	}
	term := func(reqs ...NodeSelectorRequirement) NodeSelectorTerm {
		return NodeSelectorTerm{MatchExpressions: reqs}
	}
	tests := []struct {
		name  string
		terms []NodeSelectorTerm
		want  bool
	}{
		{"In", []NodeSelectorTerm{term(req("zone", "In", "a", "b"))}, true},
		{"NotIn matches a missing label", []NodeSelectorTerm{term(req("gpu", "NotIn", "yes"))}, true},
		{"DoesNotExist", []NodeSelectorTerm{term(req("zone", "DoesNotExist"))}, false},
		{"Gt", []NodeSelectorTerm{term(req("size", "Gt", "4"))}, true},
		{"Lt", []NodeSelectorTerm{term(req("size", "Lt", "4"))}, false},
		{"terms are ORed", []NodeSelectorTerm{term(req("zone", "In", "b")), term(req("zone", "In", "a"))}, true},
		{"requirements are ANDed", []NodeSelectorTerm{term(req("zone", "In", "a"), req("size", "In", "4"))}, false},
		{"an empty term matches nothing", []NodeSelectorTerm{{}}, false},
		{"matchFields on the node name", []NodeSelectorTerm{{MatchFields: []NodeSelectorRequirement{req("metadata.name", "In", "n1")}}}, true},
	}
	for _, tt := range tests {
		sel := &NodeSelector{NodeSelectorTerms: tt.terms}
		if got := sel.Matches(node); got != tt.want {
			t.Errorf("%s: Matches = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTolerates(t *testing.T) {
	taint := Taint{Key: "dedicated", Value: "ml", Effect: "NoSchedule"}
	tests := []struct {
		name string
		tol  Toleration
		want bool
	}{
		{"Exists without a key tolerates everything", Toleration{Operator: "Exists"}, true},
		{"equal key and value", Toleration{Key: "dedicated", Operator: "Equal", Value: "ml"}, true},
		{"an empty operator means Equal", Toleration{Key: "dedicated", Value: "ml"}, true},
		{"wrong value", Toleration{Key: "dedicated", Value: "web"}, false},
		{"wrong effect", Toleration{Key: "dedicated", Operator: "Exists", Effect: "NoExecute"}, false},
		{"other key", Toleration{Key: "other", Operator: "Exists"}, false},
	}
	for _, tt := range tests {
		if got := tt.tol.Tolerates(taint); got != tt.want {
			t.Errorf("%s: Tolerates = %v, want %v", tt.name, got, tt.want)
		}
	}
	if got := taint.String(); got != "dedicated=ml:NoSchedule" {
		t.Errorf("Taint.String() = %q", got)
	}
}

func TestSelectsPod(t *testing.T) {
	owner := &Pod{Metadata: ObjectMeta{Name: "a", Namespace: "web"}}
	sameNS := &Pod{Metadata: ObjectMeta{Name: "b", Namespace: "web", Labels: map[string]string{"app": "api"}}}
	otherNS := &Pod{Metadata: ObjectMeta{Name: "c", Namespace: "jobs", Labels: map[string]string{"app": "api"}}}
	base := PodAffinityTerm{LabelSelector: &LabelSelector{MatchLabels: map[string]string{"app": "api"}}, TopologyKey: "kubernetes.io/hostname"}
	listed := base
	listed.Namespaces = []string{"jobs"}
	anyNS := base
	anyNS.NamespaceSelector = &LabelSelector{}
	noSelector := base
	noSelector.LabelSelector = nil
	tests := []struct {
		name  string
		term  PodAffinityTerm
		other *Pod
		want  bool
	}{
		{"own namespace by default", base, sameNS, true},
		{"not other namespaces by default", base, otherNS, false},
		{"listed namespaces", listed, otherNS, true},
		{"listed namespaces only", listed, sameNS, false},
		{"a namespaceSelector counts as any namespace", anyNS, otherNS, true},
		{"a nil label selector selects nothing", noSelector, sameNS, false},
	}
	for _, tt := range tests {
		if got := tt.term.SelectsPod(owner, tt.other); got != tt.want {
			t.Errorf("%s: SelectsPod = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestPodRequests(t *testing.T) {
	ctr := func(name, cpu, mem string) Container {
		return Container{Name: name, Resources: ResourceRequirements{Requests: map[string]string{"cpu": cpu, "memory": mem}}}
	}
	sidecar := func(name, cpu, mem string) Container {
		c := ctr(name, cpu, mem)
		c.RestartPolicy = "Always"
		return c
	}
	tests := []struct {
		name string
		spec PodSpec
		want Resources
	}{
		{"containers add up", PodSpec{Containers: []Container{ctr("a", "100m", "64Mi"), ctr("b", "200m", "64Mi")}}, Resources{300, 128 << 20, 1}},
		{"a heavy init step sets the peak", PodSpec{InitContainers: []Container{ctr("init", "1", "32Mi")}, Containers: []Container{ctr("a", "100m", "64Mi")}}, Resources{1000, 64 << 20, 1}},
		{"sidecars run alongside the app", PodSpec{InitContainers: []Container{sidecar("proxy", "100m", "32Mi")}, Containers: []Container{ctr("a", "100m", "64Mi")}}, Resources{200, 96 << 20, 1}},
		{"init steps count the sidecars started before them", PodSpec{InitContainers: []Container{sidecar("proxy", "100m", "32Mi"), ctr("migrate", "500m", "16Mi")}, Containers: []Container{ctr("a", "100m", "64Mi")}}, Resources{600, 96 << 20, 1}},
		{"overhead is added", PodSpec{Containers: []Container{ctr("a", "100m", "64Mi")}, Overhead: map[string]string{"cpu": "50m", "memory": "16Mi"}}, Resources{150, 80 << 20, 1}},
		{"no requests", PodSpec{Containers: []Container{{Name: "a"}}}, Resources{0, 0, 1}},
	}
	for _, tt := range tests {
		if got := PodRequests(&Pod{Spec: tt.spec}); got != tt.want {
			t.Errorf("%s: PodRequests = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestLimitRangeDefaultRequests(t *testing.T) {
	item := LimitRangeItem{Type: "Container", Default: map[string]string{"cpu": "1", "memory": "1Gi"}, DefaultRequest: map[string]string{"cpu": "250m"}}
	if got := FormatResourceList(item.DefaultRequests()); got != "cpu=250m, memory=1Gi" {
		t.Errorf("DefaultRequests = %q, want the defaultRequest CPU and the default memory limit", got)
	}
}

func TestAddDocuments(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantNodes int
		wantPods  int
		wantSkip  string
		wantErr   string
	}{
		{
			name:      "multi-document YAML",
			data:      "kind: Node\nmetadata: {name: a}\n---\n# comment only\n---\nkind: Pod\nmetadata: {name: p}\n",
			wantNodes: 1, wantPods: 1,
		},
		{
			name:     "typed list whose items carry no kind",
			data:     `{"kind":"PodList","items":[{"metadata":{"name":"p1"}},{"metadata":{"name":"p2"}}]}`,
			wantPods: 2,
		},
		{
			name:     "unknown kinds are counted, not rejected",
			data:     "kind: Service\nmetadata: {name: s}\n",
			wantSkip: "Service",
		},
		{
			name:    "object without kind",
			data:    "metadata: {name: x}\n",
			wantErr: "no kind",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			err := s.AddDocuments("test", []byte(tt.data))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Nodes) != tt.wantNodes || len(s.Pods) != tt.wantPods {
				t.Errorf("got %d nodes, %d pods; want %d, %d", len(s.Nodes), len(s.Pods), tt.wantNodes, tt.wantPods)
			}
			if tt.wantSkip != "" && s.Skipped[tt.wantSkip] != 1 {
				t.Errorf("Skipped = %v, want %s counted", s.Skipped, tt.wantSkip)
			}
		})
	}
}

func TestAddDocumentsKarpenterLimitRangeAndInfo(t *testing.T) {
	data := `apiVersion: karpenter.sh/v1
kind: NodePool
metadata: {name: general}
spec: {disruption: {consolidationPolicy: WhenEmpty, budgets: [{nodes: "0"}]}}
---
apiVersion: example.com/v1
kind: NodePool
metadata: {name: unrelated}
---
apiVersion: karpenter.sh/v1
kind: NodeClaim
metadata: {name: claim-1}
status: {nodeName: n1, conditions: [{type: Consolidatable, status: "False"}]}
---
apiVersion: v1
kind: LimitRange
metadata: {name: defaults, namespace: team}
spec: {limits: [{type: Container, defaultRequest: {cpu: 100m}}]}
---
apiVersion: kubeshrink/v1alpha1
kind: SnapshotInfo
takenAt: "2026-01-02T03:04:05Z"
serverVersion: v1.31.2
`
	s := New()
	if err := s.AddDocuments("test", []byte(data)); err != nil {
		t.Fatal(err)
	}
	if np := s.NodePool("general"); len(s.NodePools) != 1 || np == nil || np.Spec.Disruption.ConsolidationPolicy != "WhenEmpty" {
		t.Errorf("NodePools = %+v", s.NodePools)
	}
	if s.Skipped["NodePool"] != 1 {
		t.Errorf("a NodePool from another API group should be skipped: %v", s.Skipped)
	}
	if nc := s.NodeClaimForNode("n1"); nc == nil || nc.Status.Conditions[0].Status != "False" {
		t.Errorf("NodeClaim for n1 = %+v", nc)
	}
	if len(s.LimitRangesIn("team")) != 1 {
		t.Errorf("LimitRanges = %+v", s.LimitRanges)
	}
	if s.TakenAt != "2026-01-02T03:04:05Z" || s.ServerVersion != "v1.31.2" {
		t.Errorf("TakenAt = %q, ServerVersion = %q", s.TakenAt, s.ServerVersion)
	}
}

func TestValidateRejectsBadQuantities(t *testing.T) {
	tests := []struct{ name, data, want string }{
		{"node allocatable", "kind: Node\nmetadata: {name: a}\nstatus: {allocatable: {cpu: lots}}\n", "Node/a"},
		{"init container request", "kind: Pod\nmetadata: {name: p, namespace: ns}\nspec: {initContainers: [{name: init, resources: {requests: {memory: much}}}]}\n", "Pod/ns/p container init"},
		{"VPA recommendation", "kind: VerticalPodAutoscaler\nmetadata: {name: v, namespace: ns}\nstatus: {recommendation: {containerRecommendations: [{containerName: app, target: {cpu: some}}]}}\n", "VerticalPodAutoscaler/ns/v"},
	}
	for _, tt := range tests {
		s := New()
		if err := s.AddDocuments("test", []byte(tt.data)); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Validate error = %v, want one naming %s", tt.name, err, tt.want)
		}
	}
}

func TestLoadExample(t *testing.T) {
	s, err := Load("../../examples/weekend-scale-down")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{
		"nodes": len(s.Nodes), "pods": len(s.Pods), "pdbs": len(s.PDBs), "hpas": len(s.HPAs),
		"scaledObjects": len(s.ScaledObjects), "vpas": len(s.VPAs), "workloads": len(s.Workloads), "events": len(s.Events),
	}
	want := map[string]int{"nodes": 4, "pods": 11, "pdbs": 1, "hpas": 3, "scaledObjects": 1, "vpas": 1, "workloads": 5, "events": 2}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d", k, got[k], v)
		}
	}
	if w := s.Workload("ReplicaSet", "shop", "web-7d9f8"); w == nil || w.Kind != "ReplicaSet" {
		t.Errorf("ReplicaSet web-7d9f8 not loaded with its kind: %+v", w)
	}
}
