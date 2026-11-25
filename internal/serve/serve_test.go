// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package serve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/atiasadir/kubeshrink/internal/live"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

const teamExample = "../../examples/team-bin-packing"

func loadTeam(t *testing.T) *snapshot.Snapshot {
	t.Helper()
	s, err := snapshot.Load(teamExample)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func always(s *snapshot.Snapshot) func(context.Context) (*snapshot.Snapshot, error) {
	return func(context.Context) (*snapshot.Snapshot, error) { return s, nil }
}

// series flattens every kubeshrink_ series to "name{label=value,...}" -> value.
func series(t *testing.T, g prometheus.Gatherer) map[string]float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		if !strings.HasPrefix(mf.GetName(), "kubeshrink_") {
			continue
		}
		for _, m := range mf.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName()+"="+l.GetValue())
			}
			out[mf.GetName()+"{"+strings.Join(labels, ",")+"}"] = m.GetGauge().GetValue() + m.GetCounter().GetValue()
		}
	}
	return out
}

func diffSeries(t *testing.T, got, want map[string]float64) {
	t.Helper()
	keys := map[string]bool{}
	for k := range got {
		keys[k] = true
	}
	for k := range want {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		g, inGot := got[k]
		w, inWant := want[k]
		switch {
		case !inWant:
			t.Errorf("unexpected series %s = %v", k, g)
		case !inGot:
			t.Errorf("missing series %s, want %v", k, w)
		case g != w:
			t.Errorf("%s = %v, want %v", k, g, w)
		}
	}
}

func TestScanExportsTeamExample(t *testing.T) {
	s := New(Config{Load: always(loadTeam(t)), Version: "v0.0.0-test", Revision: "abc123"})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := series(t, s.metrics.Registry)
	for _, k := range []string{"kubeshrink_scan_duration_seconds{}", "kubeshrink_last_successful_scan_timestamp_seconds{}"} {
		if got[k] <= 0 {
			t.Errorf("%s = %v, want > 0", k, got[k])
		}
		delete(got, k)
	}
	diffSeries(t, got, map[string]float64{
		"kubeshrink_build_info{revision=abc123,version=v0.0.0-test}":                       1,
		"kubeshrink_nodes{nodepool=general,verdict=blocked}":                               3,
		"kubeshrink_nodes{nodepool=ml,verdict=blocked}":                                    1,
		"kubeshrink_nodes_needed{basis=requests}":                                          2,
		"kubeshrink_nodes_needed{basis=vpa-target}":                                        2,
		"kubeshrink_blocked_nodes{reason=no-room-elsewhere,severity=blocker}":              4,
		"kubeshrink_blocked_nodes{reason=karpenter-consolidation-policy,severity=blocker}": 1,
		"kubeshrink_blocked_nodes{reason=autoscaler-event,severity=possible-blocker}":      1,
		"kubeshrink_pinned_workloads{controller=hpa,namespace=checkout}":                   1,
		"kubeshrink_pinned_workloads{controller=keda,namespace=reports}":                   1,
		"kubeshrink_oversized_workloads{namespace=checkout}":                               1,
		"kubeshrink_oversized_workloads{namespace=ml}":                                     0,
		"kubeshrink_oversized_workloads{namespace=reports}":                                0,
		"kubeshrink_workloads_without_vpa{namespace=checkout}":                             0,
		"kubeshrink_workloads_without_vpa{namespace=ml}":                                   0,
		"kubeshrink_workloads_without_vpa{namespace=reports}":                              0,
	})
}

func TestNoObjectNamesInLabels(t *testing.T) {
	snap := loadTeam(t)
	s := New(Config{Load: always(snap)})
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range snap.Nodes {
		names = append(names, n.Metadata.Name)
	}
	for _, p := range snap.Pods {
		names = append(names, p.Metadata.Name)
	}
	for k := range series(t, s.metrics.Registry) {
		for _, n := range names {
			if strings.Contains(k, "="+n+",") || strings.Contains(k, "="+n+"}") {
				t.Errorf("series %s carries the object name %q", k, n)
			}
		}
	}
}

func TestHealthAndReadiness(t *testing.T) {
	snap := loadTeam(t)
	var fail atomic.Bool
	fail.Store(true)
	s := New(Config{Load: func(context.Context) (*snapshot.Snapshot, error) {
		if fail.Load() {
			return nil, errors.New("connection refused")
		}
		return snap, nil
	}})
	check := func(when, path string, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: GET %s = %d, want %d", when, path, rec.Code, want)
		}
	}
	check("before any scan", "/healthz", http.StatusOK)
	check("before any scan", "/readyz", http.StatusServiceUnavailable)
	if err := s.Scan(context.Background()); err == nil {
		t.Fatal("Scan succeeded, want the load error")
	}
	check("after a failed scan", "/readyz", http.StatusServiceUnavailable)
	fail.Store(false)
	if err := s.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	check("after a successful scan", "/readyz", http.StatusOK)
	check("after a successful scan", "/metrics", http.StatusOK)
}

func TestScanErrorsByResource(t *testing.T) {
	snap := loadTeam(t)
	snap.Unlisted = []string{"verticalpodautoscalers"}
	results := []error{
		fmt.Errorf("read cluster: %w", &live.ListError{Resource: "nodes", Err: errors.New("forbidden")}),
		errors.New("timeout"),
		nil,
	}
	i := 0
	s := New(Config{Load: func(context.Context) (*snapshot.Snapshot, error) {
		err := results[i]
		i++
		if err != nil {
			return nil, err
		}
		return snap, nil
	}})
	for range results {
		_ = s.Scan(context.Background())
	}
	got := series(t, s.metrics.Registry)
	for k, want := range map[string]float64{
		"kubeshrink_scan_errors_total{resource=nodes}":                  1,
		"kubeshrink_scan_errors_total{resource=unknown}":                1,
		"kubeshrink_scan_errors_total{resource=verticalpodautoscalers}": 1,
	} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}
}

func TestRunScansOnIntervalAndStops(t *testing.T) {
	snap := loadTeam(t)
	var scans atomic.Int32
	s := New(Config{Listen: "127.0.0.1:0", Interval: 10 * time.Millisecond, Load: func(context.Context) (*snapshot.Snapshot, error) {
		scans.Add(1)
		return snap, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	for deadline := time.Now().Add(10 * time.Second); scans.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v after cancel, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if n := scans.Load(); n < 2 {
		t.Errorf("scans = %d, want at least 2 (one at start, one per interval)", n)
	}
}
