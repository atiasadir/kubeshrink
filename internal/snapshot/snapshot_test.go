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

func TestValidateRejectsBadQuantity(t *testing.T) {
	s := New()
	if err := s.AddDocuments("test", []byte("kind: Node\nmetadata: {name: a}\nstatus: {allocatable: {cpu: lots}}\n")); err != nil {
		t.Fatal(err)
	}
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), "Node/a") {
		t.Fatalf("Validate error = %v, want one naming Node/a", err)
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
