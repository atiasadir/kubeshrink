// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package live reads the objects KubeShrink needs from a running cluster.
//
// It only ever calls list (via the dynamic client) on the resources in
// Resources. Secrets and ConfigMaps are deliberately not in that list, and
// nothing here creates, updates, patches or deletes anything.
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// Resource is one kind KubeShrink lists. Required kinds abort the run when
// they cannot be read; the rest are recorded as data gaps.
type Resource struct {
	GVR      schema.GroupVersionResource
	Kind     string
	Required bool
}

// Resources is the complete set of reads. Keep docs/rbac.yaml in sync.
var Resources = []Resource{
	{schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "Node", true},
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", true},
	{schema.GroupVersionResource{Version: "v1", Resource: "events"}, "Event", false},
	{schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, "PodDisruptionBudget", false},
	{schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, "HorizontalPodAutoscaler", false},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", false},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", false},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet", false},
	{schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}, "ScaledObject", false},
	{schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}, "VerticalPodAutoscaler", false},
}

type Options struct {
	Kubeconfig string
	// Context must be named explicitly; there is no fallback to the current context.
	Context string
	// SaveDir, when set, receives the raw lists as JSON so the run can be replayed offline.
	SaveDir string
}

// Load builds a read-only client for the named context and fetches a snapshot.
func Load(ctx context.Context, opts Options) (*snapshot.Snapshot, error) {
	cfg, err := RESTConfig(opts.Kubeconfig, opts.Context)
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return Fetch(ctx, client, opts.SaveDir)
}

func RESTConfig(kubeconfig, contextName string) (*rest.Config, error) {
	if contextName == "" {
		return nil, fmt.Errorf("a kubeconfig context must be named explicitly")
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: contextName})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, err
	}
	if _, ok := raw.Contexts[contextName]; !ok {
		return nil, fmt.Errorf("context %q not found in kubeconfig", contextName)
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.UserAgent = "kubeshrink (read-only)"
	cfg.QPS, cfg.Burst = 20, 40
	return cfg, nil
}

// Fetch lists every resource in Resources and decodes it into a snapshot.
func Fetch(ctx context.Context, client dynamic.Interface, saveDir string) (*snapshot.Snapshot, error) {
	if saveDir != "" {
		if err := os.MkdirAll(saveDir, 0o700); err != nil {
			return nil, err
		}
	}
	s := snapshot.New()
	for _, r := range Resources {
		items, err := listAll(ctx, client, r.GVR)
		if err != nil {
			if !r.Required && (apierrors.IsNotFound(err) || apierrors.IsForbidden(err)) {
				s.Notes = append(s.Notes, fmt.Sprintf("Could not list %s (%s); rules that use it had no data.", r.GVR.Resource, reason(err)))
				continue
			}
			return nil, fmt.Errorf("list %s: %w", r.GVR.Resource, err)
		}
		for i, raw := range items {
			if err := s.AddObject(raw, r.Kind); err != nil {
				return nil, fmt.Errorf("%s item %d: %w", r.GVR.Resource, i, err)
			}
		}
		if saveDir != "" {
			if err := saveList(saveDir, r, items); err != nil {
				return nil, err
			}
		}
	}
	return s, s.Validate()
}

func reason(err error) string {
	switch {
	case apierrors.IsNotFound(err):
		return "not served by this cluster"
	case apierrors.IsForbidden(err):
		return "forbidden by RBAC"
	}
	return err.Error()
}

func listAll(ctx context.Context, client dynamic.Interface, gvr schema.GroupVersionResource) ([]json.RawMessage, error) {
	var out []json.RawMessage
	cont := ""
	for {
		list, err := client.Resource(gvr).List(ctx, metav1.ListOptions{Limit: 500, Continue: cont})
		if err != nil {
			return nil, err
		}
		for i := range list.Items {
			b, err := list.Items[i].MarshalJSON()
			if err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		if cont = list.GetContinue(); cont == "" {
			return out, nil
		}
	}
}

func saveList(dir string, r Resource, items []json.RawMessage) error {
	if items == nil {
		items = []json.RawMessage{}
	}
	data, err := json.MarshalIndent(map[string]any{"apiVersion": "v1", "kind": r.Kind + "List", "items": items}, "", "  ")
	if err != nil {
		return err
	}
	name := r.GVR.Resource
	if r.GVR.Group != "" {
		name += "." + r.GVR.Group
	}
	return os.WriteFile(filepath.Join(dir, name+".json"), data, 0o600)
}
