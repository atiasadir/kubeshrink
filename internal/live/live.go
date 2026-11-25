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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// Fallback is listed instead when GVR is not served, for kinds whose API
	// version changed (Karpenter's v1beta1 to v1).
	Fallback *schema.GroupVersionResource
}

// Resources is the complete set of reads. Keep docs/rbac.yaml in sync.
var Resources = []Resource{
	{schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "Node", true, nil},
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", true, nil},
	{schema.GroupVersionResource{Version: "v1", Resource: "events"}, "Event", false, nil},
	{schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, "PodDisruptionBudget", false, nil},
	{schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, "HorizontalPodAutoscaler", false, nil},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", false, nil},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", false, nil},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet", false, nil},
	{schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}, "ScaledObject", false, nil},
	{schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}, "VerticalPodAutoscaler", false, nil},
	{schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}, "NodePool", false, &schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1beta1", Resource: "nodepools"}},
	{schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}, "NodeClaim", false, &schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1beta1", Resource: "nodeclaims"}},
}

// Options says which cluster to read and where to save what was read.
type Options struct {
	Kubeconfig string
	// Context must be named explicitly; there is no fallback to the current context.
	Context string
	// InCluster uses the pod's service account instead of a kubeconfig context.
	InCluster bool
	// SaveDir, when set, receives the raw lists as JSON so the run can be replayed offline.
	SaveDir string
}

// Load builds a read-only client for the named context and fetches a snapshot.
func Load(ctx context.Context, opts Options) (*snapshot.Snapshot, error) {
	var cfg *rest.Config
	var err error
	if opts.InCluster {
		if cfg, err = rest.InClusterConfig(); err == nil {
			tune(cfg)
		}
	} else {
		cfg, err = RESTConfig(opts.Kubeconfig, opts.Context)
	}
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	s, err := Fetch(ctx, client, opts.SaveDir)
	if err != nil {
		return nil, err
	}
	if v, err := serverVersion(ctx, cfg); err == nil {
		s.ServerVersion = v
	}
	if opts.SaveDir != "" && s.ServerVersion != "" {
		if err := writeInfo(opts.SaveDir, s); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// RESTConfig builds a client configuration for a context that must be named
// explicitly, from the given kubeconfig file or, when that is empty, from
// $KUBECONFIG or ~/.kube/config. The current context is never used implicitly.
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
	tune(cfg)
	return cfg, nil
}

func tune(cfg *rest.Config) {
	cfg.UserAgent = "kubeshrink (read-only)"
	cfg.QPS, cfg.Burst = 20, 40
	// Bound every request, so a slow API server cannot hang a run.
	cfg.Timeout = 30 * time.Second
}

// ListError says which resource could not be listed.
type ListError struct {
	Resource string
	Err      error
}

func (e *ListError) Error() string { return "list " + e.Resource + ": " + e.Err.Error() }

func (e *ListError) Unwrap() error { return e.Err }

// Fetch lists every resource in Resources and decodes it into a snapshot.
func Fetch(ctx context.Context, client dynamic.Interface, saveDir string) (*snapshot.Snapshot, error) {
	if saveDir != "" {
		if err := os.MkdirAll(saveDir, 0o700); err != nil {
			return nil, err
		}
	}
	s := snapshot.New()
	s.TakenAt = time.Now().UTC().Format(time.RFC3339)
	for _, r := range Resources {
		items, err := listAll(ctx, client, r.GVR)
		if err != nil && r.Fallback != nil && apierrors.IsNotFound(err) {
			items, err = listAll(ctx, client, *r.Fallback)
		}
		if err != nil {
			if !r.Required && (apierrors.IsNotFound(err) || apierrors.IsForbidden(err)) {
				s.Notes = append(s.Notes, fmt.Sprintf("Could not list %s (%s); rules that use it had no data.", r.GVR.Resource, reason(err)))
				if apierrors.IsForbidden(err) {
					s.Unlisted = append(s.Unlisted, r.GVR.Resource)
				}
				continue
			}
			return nil, &ListError{Resource: r.GVR.Resource, Err: err}
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
	if saveDir != "" {
		if err := writeInfo(saveDir, s); err != nil {
			return nil, err
		}
	}
	return s, s.Validate()
}

// serverVersion reads GET /version, which is public cluster information. It
// only labels the report, so callers ignore its errors.
func serverVersion(ctx context.Context, cfg *rest.Config) (string, error) {
	hc, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(cfg.Host, "/")+"/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /version: %s", resp.Status)
	}
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// writeInfo records when the snapshot was taken and from which Kubernetes
// version, so an offline replay can say so.
func writeInfo(dir string, s *snapshot.Snapshot) error {
	data, err := json.MarshalIndent(map[string]string{"kind": snapshot.InfoKind, "takenAt": s.TakenAt, "serverVersion": s.ServerVersion}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshot.InfoFile), data, 0o600)
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
