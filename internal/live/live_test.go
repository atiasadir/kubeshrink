// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package live

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

func fakeClient(objs ...runtime.Object) *fake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, r := range Resources {
		listKinds[r.GVR] = r.Kind + "List"
		if r.Fallback != nil {
			listKinds[*r.Fallback] = r.Kind + "List"
		}
	}
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
}

func obj(apiVersion, kind, ns, name string, extra map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name}
	if ns != "" {
		meta["namespace"] = ns
	}
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": meta}
	for k, v := range extra {
		o[k] = v
	}
	return &unstructured.Unstructured{Object: o}
}

func deny(err error) k8stesting.ReactionFunc {
	return func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, err }
}

func TestFetchIsReadOnlyAndRecordsGaps(t *testing.T) {
	client := fakeClient(
		obj("v1", "Node", "", "n1", map[string]any{"status": map[string]any{"allocatable": map[string]any{"cpu": "2", "memory": "4Gi"}}}),
		obj("v1", "Pod", "default", "p1", map[string]any{"spec": map[string]any{"nodeName": "n1"}}),
	)
	client.PrependReactor("list", "scaledobjects", deny(
		apierrors.NewNotFound(schema.GroupResource{Group: "keda.sh", Resource: "scaledobjects"}, "")))
	client.PrependReactor("list", "events", deny(
		apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("denied"))))

	dir := t.TempDir()
	s, err := Fetch(context.Background(), client, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 1 || len(s.Pods) != 1 {
		t.Fatalf("got %d nodes, %d pods; want 1, 1", len(s.Nodes), len(s.Pods))
	}
	if len(s.Notes) != 2 {
		t.Errorf("notes = %v, want one for scaledobjects and one for events", s.Notes)
	}

	for _, a := range client.Actions() {
		if a.GetVerb() != "list" {
			t.Errorf("unexpected verb %q on %s", a.GetVerb(), a.GetResource().Resource)
		}
		if r := a.GetResource().Resource; r == "secrets" || r == "configmaps" {
			t.Errorf("must never read %s", r)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "nodes.json")); err != nil {
		t.Fatalf("saved snapshot missing: %v", err)
	}
	replayed, err := snapshot.Load(dir)
	if err != nil {
		t.Fatalf("saved snapshot does not load offline: %v", err)
	}
	if len(replayed.Nodes) != 1 || len(replayed.Pods) != 1 {
		t.Errorf("replayed %d nodes, %d pods; want 1, 1", len(replayed.Nodes), len(replayed.Pods))
	}
}

func TestFetchStopsWhenRequiredKindIsForbidden(t *testing.T) {
	client := fakeClient()
	client.PrependReactor("list", "nodes", deny(
		apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("denied"))))
	if _, err := Fetch(context.Background(), client, ""); err == nil {
		t.Fatal("expected an error when nodes cannot be listed")
	}
}

func TestResourcesNeverIncludeSecretsOrConfigMaps(t *testing.T) {
	for _, r := range Resources {
		if r.GVR.Resource == "secrets" || r.GVR.Resource == "configmaps" {
			t.Errorf("Resources must not include %s", r.GVR.Resource)
		}
	}
}

func TestRESTConfigRequiresExplicitKnownContext(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	data := `apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://127.0.0.1:6443"}}]
users: [{name: u, user: {token: synthetic}}]
contexts: [{name: dev, context: {cluster: c, user: u}}]
current-context: dev
`
	if err := os.WriteFile(kubeconfig, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RESTConfig(kubeconfig, ""); err == nil {
		t.Error("an empty context must be rejected even when current-context is set")
	}
	if _, err := RESTConfig(kubeconfig, "missing"); err == nil {
		t.Error("an unknown context must be rejected")
	}
	cfg, err := RESTConfig(kubeconfig, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "https://127.0.0.1:6443" || cfg.UserAgent != "kubeshrink (read-only)" {
		t.Errorf("unexpected config: host %q, user agent %q", cfg.Host, cfg.UserAgent)
	}
}
