// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package live

import (
	"os"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type rule struct {
	APIGroups []string `json:"apiGroups"`
	Resources []string `json:"resources"`
	Verbs     []string `json:"verbs"`
}

// grants flattens RBAC rules to "group/resource" -> verbs and fails on any
// verb beyond get and list, on wildcards, and on Secrets or ConfigMaps.
func grants(t *testing.T, source string, rules []rule) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, r := range rules {
		verbs := append([]string(nil), r.Verbs...)
		sort.Strings(verbs)
		if strings.Join(verbs, ",") != "get,list" {
			t.Errorf("%s: verbs %v, want exactly get and list", source, r.Verbs)
		}
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				if res == "secrets" || res == "configmaps" || res == "*" || g == "*" {
					t.Errorf("%s: must not grant %q in group %q", source, res, g)
				}
				out[g+"/"+res] = strings.Join(verbs, ",")
			}
		}
	}
	return out
}

// TestRBACMatchesResources keeps the documented ClusterRole and the Helm
// chart's rules equal to what live mode reads: nothing missing, nothing extra.
func TestRBACMatchesResources(t *testing.T) {
	want := map[string]bool{}
	for _, r := range Resources {
		want[r.GVR.Group+"/"+r.GVR.Resource] = true
		if r.Fallback != nil {
			want[r.Fallback.Group+"/"+r.Fallback.Resource] = true
		}
	}

	doc, err := os.ReadFile("../../docs/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role struct {
		Kind  string `json:"kind"`
		Rules []rule `json:"rules"`
	}
	if err := yaml.Unmarshal([]byte(strings.Split(string(doc), "\n---")[0]), &role); err != nil {
		t.Fatal(err)
	}
	if role.Kind != "ClusterRole" {
		t.Fatalf("docs/rbac.yaml: first document is %q, want ClusterRole", role.Kind)
	}

	chart, err := os.ReadFile("../../charts/kubeshrink/files/clusterrole-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var chartRules []rule
	if err := yaml.Unmarshal(chart, &chartRules); err != nil {
		t.Fatal(err)
	}

	for source, rules := range map[string][]rule{"docs/rbac.yaml": role.Rules, "chart": chartRules} {
		got := grants(t, source, rules)
		for k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("%s: missing get/list on %s, which live mode reads", source, k)
			}
		}
		for k := range got {
			if !want[k] {
				t.Errorf("%s: grants %s, which live mode never reads", source, k)
			}
		}
	}
}
