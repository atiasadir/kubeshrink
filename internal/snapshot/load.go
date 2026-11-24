// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Snapshot is an in-memory view of the objects KubeShrink understands.
// Nothing in KubeShrink writes back to a cluster.
type Snapshot struct {
	Nodes         []Node
	Pods          []Pod
	PDBs          []PodDisruptionBudget
	HPAs          []HorizontalPodAutoscaler
	ScaledObjects []ScaledObject
	VPAs          []VerticalPodAutoscaler
	Workloads     []Workload
	Events        []Event
	// Skipped counts objects of kinds KubeShrink does not use yet.
	Skipped map[string]int
	// Notes records gaps in the data, e.g. a kind the live reader could not list.
	Notes []string
}

func New() *Snapshot { return &Snapshot{Skipped: map[string]int{}} }

// Load reads a single file or every .yaml/.yml/.json file under a directory.
// Files may hold multiple YAML documents and kubectl-style "List" objects,
// e.g. the output of `kubectl get nodes,pods,pdb,hpa -A -o yaml`.
func Load(path string) (*Snapshot, error) {
	files, err := snapshotFiles(path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no .yaml, .yml or .json files found in %s", path)
	}
	s := New()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if err := s.AddDocuments(f, data); err != nil {
			return nil, err
		}
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func snapshotFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".yaml", ".yml", ".json":
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// AddDocuments parses YAML or JSON data that may contain several documents.
func (s *Snapshot) AddDocuments(source string, data []byte) error {
	for i, doc := range splitDocuments(data) {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		js, err := yaml.YAMLToJSON(doc)
		if err != nil {
			return fmt.Errorf("%s: document %d: %w", source, i+1, err)
		}
		if bytes.Equal(bytes.TrimSpace(js), []byte("null")) {
			continue
		}
		if err := s.AddObject(js, ""); err != nil {
			return fmt.Errorf("%s: document %d: %w", source, i+1, err)
		}
	}
	return nil
}

func splitDocuments(data []byte) [][]byte {
	var docs [][]byte
	var cur bytes.Buffer
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if bytes.Equal(bytes.TrimRight(line, " \t\r\n"), []byte("---")) {
			docs = append(docs, append([]byte(nil), cur.Bytes()...))
			cur.Reset()
			continue
		}
		cur.Write(line)
	}
	return append(docs, cur.Bytes())
}

// AddObject decodes one JSON object (or List) into the snapshot. defaultKind is
// used when the object itself carries no kind, as with typed List items.
func (s *Snapshot) AddObject(raw []byte, defaultKind string) error {
	var head struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return err
	}
	kind := head.Kind
	if kind == "" {
		kind = defaultKind
	}
	if strings.HasSuffix(kind, "List") {
		itemKind := strings.TrimSuffix(kind, "List") // "PodList" -> "Pod"; plain "List" -> ""
		for i, item := range head.Items {
			if err := s.AddObject(item, itemKind); err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
		}
		return nil
	}
	switch kind {
	case "Node":
		return decodeInto(raw, &s.Nodes)
	case "Pod":
		return decodeInto(raw, &s.Pods)
	case "PodDisruptionBudget":
		return decodeInto(raw, &s.PDBs)
	case "HorizontalPodAutoscaler":
		return decodeInto(raw, &s.HPAs)
	case "ScaledObject":
		return decodeInto(raw, &s.ScaledObjects)
	case "VerticalPodAutoscaler":
		return decodeInto(raw, &s.VPAs)
	case "Deployment", "StatefulSet", "ReplicaSet":
		if err := decodeInto(raw, &s.Workloads); err != nil {
			return err
		}
		s.Workloads[len(s.Workloads)-1].Kind = kind
		return nil
	case "Event":
		return decodeInto(raw, &s.Events)
	case "":
		return errors.New("object has no kind")
	default:
		s.Skipped[kind]++
		return nil
	}
}

func decodeInto[T any](raw []byte, dst *[]T) error {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	*dst = append(*dst, v)
	return nil
}

// Validate checks the resource quantities the rules depend on, so the rules
// themselves can assume well-formed numbers.
func (s *Snapshot) Validate() error {
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if n.Metadata.Name == "" {
			return errors.New("a Node has no metadata.name")
		}
		for _, k := range trackedResources {
			if v, ok := n.Status.Allocatable[k]; ok {
				if _, err := ParseQuantity(v); err != nil {
					return fmt.Errorf("%s .status.allocatable.%s: %w", Ref("Node", "", n.Metadata.Name), k, err)
				}
			}
		}
	}
	for i := range s.Pods {
		p := &s.Pods[i]
		for _, c := range p.Spec.Containers {
			for _, k := range trackedResources {
				if v, ok := c.Resources.Requests[k]; ok {
					if _, err := ParseQuantity(v); err != nil {
						return fmt.Errorf("%s container %s requests.%s: %w",
							Ref("Pod", p.Metadata.Namespace, p.Metadata.Name), c.Name, k, err)
					}
				}
			}
		}
	}
	return nil
}

var trackedResources = []string{"cpu", "memory", "pods"}
