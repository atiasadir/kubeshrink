// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package serve

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/explain"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// metricContract is every metric KubeShrink exports, with its type and its
// label names in sorted order. Dashboards and alerts are built on these, so
// changing one is a breaking change: update docs/metrics.md and CHANGELOG.md
// in the same pull request.
var metricContract = map[string]struct {
	kind   string
	labels []string
}{
	"kubeshrink_build_info":                             {"GAUGE", []string{"revision", "version"}},
	"kubeshrink_last_successful_scan_timestamp_seconds": {"GAUGE", nil},
	"kubeshrink_scan_duration_seconds":                  {"GAUGE", nil},
	"kubeshrink_scan_errors_total":                      {"COUNTER", []string{"resource"}},
	"kubeshrink_nodes":                                  {"GAUGE", []string{"nodepool", "verdict"}},
	"kubeshrink_nodes_needed":                           {"GAUGE", []string{"basis"}},
	"kubeshrink_blocked_nodes":                          {"GAUGE", []string{"reason", "severity"}},
	"kubeshrink_pinned_workloads":                       {"GAUGE", []string{"controller", "namespace"}},
	"kubeshrink_oversized_workloads":                    {"GAUGE", []string{"namespace"}},
	"kubeshrink_workloads_without_vpa":                  {"GAUGE", []string{"namespace"}},
}

// boundedLabels lists the only values these labels may take, which keeps a
// scrape small however large the cluster is. TestNoObjectNamesInLabels covers
// the open-ended labels (namespace, nodepool).
func boundedLabels() map[string][]string {
	reasons := make([]string, 0, len(explain.Catalog))
	for _, r := range explain.Catalog {
		reasons = append(reasons, r.ID)
	}
	return map[string][]string{
		"verdict":    {explain.VerdictBlocked, explain.VerdictPossiblyBlocked, explain.VerdictNoBlockerFound},
		"severity":   {string(explain.Blocker), string(explain.PossibleBlocker)},
		"basis":      {"requests", "vpa-target"},
		"controller": {"hpa", "keda", "keda-paused"},
		"reason":     reasons,
	}
}

// TestMetricsContract scans the team example once with a failing list and once
// successfully, then checks every exported series against the contract.
func TestMetricsContract(t *testing.T) {
	snap := loadTeam(t)
	failed := false
	s := New(Config{Load: func(context.Context) (*snapshot.Snapshot, error) {
		if !failed {
			failed = true
			return nil, errors.New("list failed")
		}
		return snap, nil
	}})
	if err := s.Scan(context.Background()); err == nil {
		t.Fatal("the first scan should fail")
	}
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	mfs, err := s.metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	bounded := boundedLabels()
	seen := map[string]bool{}
	for _, mf := range mfs {
		name := mf.GetName()
		if !strings.HasPrefix(name, "kubeshrink_") {
			continue
		}
		want, ok := metricContract[name]
		if !ok {
			t.Errorf("%s is not in the contract: add it here and to docs/metrics.md", name)
			continue
		}
		seen[name] = true
		if got := mf.GetType().String(); got != want.kind {
			t.Errorf("%s is a %s, want %s", name, got, want.kind)
		}
		for _, m := range mf.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName())
				if allowed, ok := bounded[l.GetName()]; ok && !slices.Contains(allowed, l.GetValue()) {
					t.Errorf("%s has %s=%q, which is not one of %v", name, l.GetName(), l.GetValue(), allowed)
				}
			}
			if !slices.Equal(labels, want.labels) {
				t.Errorf("%s has labels %v, want %v", name, labels, want.labels)
			}
		}
	}
	for name := range metricContract {
		if !seen[name] {
			t.Errorf("%s was not exported", name)
		}
	}
}
