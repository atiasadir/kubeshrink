//go:build e2e

// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package e2e runs KubeShrink against a real, throwaway cluster: first the
// read-only CLI on the local demo workloads, then the Helm chart in
// in-cluster mode. `make e2e` creates a kind cluster for it and deletes it
// afterwards; see run.sh.
package e2e

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	kubeconfig  = flag.String("kubeconfig", "", "kubeconfig file of the test cluster")
	kubeContext = flag.String("context", "", "context of the test cluster in that file")
	imageRepo   = flag.String("image-repository", "kubeshrink", "repository of the kubeshrink image loaded into the cluster")
	imageTag    = flag.String("image-tag", "e2e", "tag of the kubeshrink image loaded into the cluster")
)

const (
	demoManifest     = "../../examples/local-demo/workloads.yaml"
	demoNamespace    = "kubeshrink-demo"
	chartDir         = "../../charts/kubeshrink"
	release          = "kubeshrink"
	releaseNamespace = "kubeshrink"
)

// demoRules are the rules the local demo workloads must trigger.
var demoRules = []string{
	"pdb-zero-disruptions",     // the PDB needs both replicas of api
	"replica-floor",            // the HPA holds api at its minimum of 2
	"downscaler-vs-hpa-floor",  // downtime-replicas 0 is below that minimum
	"pod-without-controller",   // the hand-started scratchpad pod
	"local-storage",            // its emptyDir volume
	"karpenter-do-not-disrupt", // its do-not-disrupt annotation
}

// cli is the kubeshrink binary built by TestMain.
var cli string

func TestMain(m *testing.M) {
	flag.Parse()
	if *kubeconfig == "" || *kubeContext == "" {
		fmt.Fprintln(os.Stderr, "e2e: -kubeconfig and -context are required; run the tests with `make e2e`")
		os.Exit(2)
	}
	dir, err := os.MkdirTemp("", "kubeshrink-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cli = filepath.Join(dir, "kubeshrink")
	if out, err := exec.Command("go", "build", "-o", cli, "../../cmd/kubeshrink").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: building kubeshrink: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// TestCLI applies the demo workloads, then checks that a live read-only scan,
// and a replay of the snapshot it saves, report the rules they should.
func TestCLI(t *testing.T) {
	kubectl(t, "apply", "-f", demoManifest)
	kubectl(t, "rollout", "status", "deployment/api", "-n", demoNamespace, "--timeout=120s")
	kubectl(t, "wait", "--for=condition=Ready", "pod/scratchpad", "-n", demoNamespace, "--timeout=120s")

	cluster := []string{"--kubeconfig", *kubeconfig, "--context", *kubeContext}
	// The PDB controller fills in disruptionsAllowed a moment after the pods
	// are ready, so poll until every expected rule shows up.
	var found map[string]bool
	eventually(t, time.Minute, func() bool {
		found = rulesIn(t, run(t, cli, append([]string{"explain", "nodes", "-o", "json"}, cluster...)...))
		return len(missing(found, demoRules)) == 0
	}, func() string { return fmt.Sprintf("the live scan is missing %v", missing(found, demoRules)) })

	dir := filepath.Join(t.TempDir(), "snapshot")
	run(t, cli, append([]string{"snapshot", "--out", dir}, cluster...)...)
	replayed := rulesIn(t, run(t, cli, "explain", "nodes", "-f", dir, "-o", "json"))
	if m := missing(replayed, demoRules); len(m) > 0 {
		t.Errorf("replaying the snapshot is missing %v", m)
	}
}

// TestChart installs the chart with the image loaded into the cluster, checks
// /metrics, the JSON logs and the service account's permissions, and checks
// that uninstalling leaves nothing behind. It runs after TestCLI, so the demo
// workloads are there to be found.
func TestChart(t *testing.T) {
	helm(t, "install", release, chartDir, "--namespace", releaseNamespace, "--create-namespace",
		"--set", "image.repository="+*imageRepo, "--set", "image.tag="+*imageTag,
		"--set", "image.pullPolicy=Never", "--set", "serve.interval=10s")
	kubectl(t, "rollout", "status", "deployment/"+release, "-n", releaseNamespace, "--timeout=120s")

	t.Run("metrics", func(t *testing.T) {
		path := "/api/v1/namespaces/" + releaseNamespace + "/services/" + release + ":metrics/proxy/metrics"
		var metrics string
		eventually(t, time.Minute, func() bool {
			metrics, _ = try("kubectl", kubectlArgs("get", "--raw", path)...)
			return strings.Contains(metrics, "kubeshrink_blocked_nodes{")
		}, func() string { return "no scan results in /metrics:\n" + metrics })
		for _, want := range []string{
			"kubeshrink_build_info{",
			"kubeshrink_scan_duration_seconds ",
			`kubeshrink_blocked_nodes{reason="pdb-zero-disruptions"`,
			`kubeshrink_blocked_nodes{reason="karpenter-do-not-disrupt"`,
			`kubeshrink_pinned_workloads{controller="hpa",namespace="` + demoNamespace + `"}`,
		} {
			if !strings.Contains(metrics, want) {
				t.Errorf("/metrics has no %s", want)
			}
		}
	})

	t.Run("JSON logs", func(t *testing.T) {
		logs := kubectl(t, "logs", "deployment/"+release, "-n", releaseNamespace)
		scans := 0
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			var entry struct {
				Msg string `json:"msg"`
			}
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Errorf("log line is not JSON: %q", line)
				continue
			}
			if entry.Msg == "scan complete" {
				scans++
			}
		}
		if scans == 0 {
			t.Errorf("no \"scan complete\" line in the logs:\n%s", logs)
		}
	})

	t.Run("read-only permissions", func(t *testing.T) {
		serviceAccount := "system:serviceaccount:" + releaseNamespace + ":" + release
		for _, c := range []struct{ verb, resource, want string }{
			{"list", "pods", "yes"},
			{"list", "nodes", "yes"},
			{"list", "secrets", "no"},
			{"list", "configmaps", "no"},
			{"watch", "pods", "no"},
			{"delete", "pods", "no"},
			{"create", "pods", "no"},
		} {
			// can-i exits with 1 when the answer is no, so only its output counts.
			out, _ := try("kubectl", kubectlArgs("auth", "can-i", c.verb, c.resource, "--all-namespaces", "--as", serviceAccount)...)
			if got := strings.TrimSpace(out); got != c.want {
				t.Errorf("can it %s %s? %q, want %q", c.verb, c.resource, got, c.want)
			}
		}
	})

	t.Run("uninstall leaves nothing behind", func(t *testing.T) {
		helm(t, "uninstall", release, "--namespace", releaseNamespace)
		var left []string
		eventually(t, 90*time.Second, func() bool {
			left = leftovers(t)
			return len(left) == 0
		}, func() string { return fmt.Sprintf("still there: %v", left) })
	})
}

// leftovers lists what remains of the release: objects in its namespace other
// than the two that Kubernetes creates in every namespace, and cluster-wide
// RBAC objects named after it.
func leftovers(t *testing.T) []string {
	t.Helper()
	var left []string
	for _, name := range strings.Fields(kubectl(t, "get", "all,serviceaccounts,roles,rolebindings,configmaps,secrets", "-n", releaseNamespace, "-o", "name")) {
		if name != "serviceaccount/default" && name != "configmap/kube-root-ca.crt" {
			left = append(left, name)
		}
	}
	for _, name := range strings.Fields(kubectl(t, "get", "clusterroles,clusterrolebindings", "-o", "name")) {
		if strings.Contains(name, release) {
			left = append(left, name)
		}
	}
	return left
}

// rulesIn returns the value of every "rule" field in a JSON report, however
// deeply it is nested.
func rulesIn(t *testing.T, report string) map[string]bool {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(report), &doc); err != nil {
		t.Fatalf("the report is not JSON: %v", err)
	}
	rules := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, value := range v {
				if rule, ok := value.(string); ok && key == "rule" {
					rules[rule] = true
				}
				walk(value)
			}
		case []any:
			for _, value := range v {
				walk(value)
			}
		}
	}
	walk(doc)
	return rules
}

// missing returns the rules in want that found does not have.
func missing(found map[string]bool, want []string) []string {
	var out []string
	for _, rule := range want {
		if !found[rule] {
			out = append(out, rule)
		}
	}
	return out
}

// eventually calls check every two seconds until it returns true, and fails
// the test with describe() if that takes longer than timeout.
func eventually(t *testing.T, timeout time.Duration, check func() bool, describe func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("after %s, %s", timeout, describe())
		}
		time.Sleep(2 * time.Second)
	}
}

// kubectlArgs and helmArgs name the test cluster on every call, so the tests
// never act on another cluster, whatever the current context is.
func kubectlArgs(args ...string) []string {
	return append([]string{"--kubeconfig", *kubeconfig, "--context", *kubeContext}, args...)
}

func helmArgs(args ...string) []string {
	return append([]string{"--kubeconfig", *kubeconfig, "--kube-context", *kubeContext}, args...)
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	return run(t, "kubectl", kubectlArgs(args...)...)
}

func helm(t *testing.T, args ...string) string {
	t.Helper()
	return run(t, "helm", helmArgs(args...)...)
}

// run runs a command and returns its standard output, failing the test if the
// command fails.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := try(name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return out
}

// try runs a command and returns its standard output, with an error that
// includes its standard error if it fails.
func try(name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
