// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/live"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

const (
	example = "../../examples/weekend-scale-down"
	team    = "../../examples/team-bin-packing"
)

func noLive(context.Context, live.Options) (*snapshot.Snapshot, error) {
	return nil, errors.New("live mode must not be used in this test")
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string
		wantStderr string
	}{
		{"no args", nil, 2, "", "Usage:"},
		{"help", []string{"help"}, 0, "Usage:", ""},
		{"version", []string{"version"}, 0, "kubeshrink dev", ""},
		{"rules", []string{"rules"}, 0, "pdb-zero-disruptions", ""},
		{"unknown command", []string{"shrink-it"}, 2, "", "unknown command"},
		{"explain one node", []string{"explain", "node", "node-a", "-f", example}, 0, "Node node-a: BLOCKED", ""},
		{"flags before the name", []string{"explain", "--snapshot", example, "node", "node-d"}, 0, "NO-BLOCKER-FOUND", ""},
		{"explain all nodes", []string{"explain", "nodes", "-f", example}, 0, "node-c", ""},
		{"unknown node", []string{"explain", "node", "nope", "-f", example}, 1, "", "not found"},
		{"explain cluster", []string{"explain", "cluster", "-f", team}, 0, "would fit on 2", ""},
		{"explain a team", []string{"explain", "team", "checkout", "-f", team}, 0, "Namespace checkout", ""},
		{"namespace alias", []string{"explain", "ns", "ml", "-f", team, "-o", "json"}, 0, "\"namespace\": \"ml\"", ""},
		{"team without a name", []string{"explain", "team", "-f", team}, 2, "", "usage: kubeshrink explain"},
		{"bad node autoscaler", []string{"explain", "cluster", "-f", team, "--node-autoscaler", "maybe"}, 2, "", "--node-autoscaler"},
		{"serve takes no snapshot", []string{"serve", "-f", team}, 2, "", "usage: kubeshrink serve"},
		{"serve interval too short", []string{"serve", "--interval", "1s"}, 2, "", "at least 10s"},
		{"rules as JSON", []string{"rules", "-o", "json"}, 0, "\"layer\": \"policy\"", ""},
		{"no source", []string{"explain", "node", "node-a"}, 1, "", "a source is required"},
		{"two sources", []string{"explain", "node", "node-a", "-f", example, "--context", "x"}, 1, "", "not both"},
		{"bad output", []string{"explain", "node", "node-a", "-f", example, "-o", "xml"}, 2, "", "text or json"},
		{"bad subcommand", []string{"explain", "pod", "x", "-f", example}, 2, "", "usage:"},
		{"snapshot needs out", []string{"snapshot", "--context", "x"}, 2, "", "usage:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut, noLive)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantCode, errOut.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout missing %q:\n%s", tt.wantOut, out.String())
			}
			if !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantStderr, errOut.String())
			}
		})
	}
}

func TestExplainJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"explain", "node", "node-c", "-f", example, "-o", "json"}, &out, &errOut, noLive); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var r struct {
		Verdict  string `json:"verdict"`
		Findings []struct {
			Rule     string `json:"rule"`
			Evidence []any  `json:"evidence"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "blocked" || len(r.Findings) == 0 {
		t.Errorf("unexpected report: %+v", r)
	}
}

func TestLiveModeUsesExplicitContext(t *testing.T) {
	var gotOpts live.Options
	fakeLive := func(_ context.Context, o live.Options) (*snapshot.Snapshot, error) {
		gotOpts = o
		return snapshot.Load(example)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"explain", "nodes", "--context", "kind-dev", "--kubeconfig", "/tmp/kc"}, &out, &errOut, fakeLive)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if gotOpts.Context != "kind-dev" || gotOpts.Kubeconfig != "/tmp/kc" {
		t.Errorf("live options = %+v", gotOpts)
	}
	if !strings.Contains(errOut.String(), `reading context "kind-dev" read-only`) {
		t.Errorf("missing read-only notice: %s", errOut.String())
	}
}

func TestServeNeedsContextOutsideCluster(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	var out, errOut bytes.Buffer
	if code := run([]string{"serve"}, &out, &errOut, noLive); code != 2 || !strings.Contains(errOut.String(), "needs --context") {
		t.Fatalf("code %d, stderr %q; want 2 and a hint to pass --context", code, errOut.String())
	}
}
