// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// update rewrites the golden files instead of comparing with them. Run it
// through `make goldens` after an intended output change, then review the diff.
var update = flag.Bool("update", false, "rewrite the golden files in testdata/golden")

const goldenDir = "testdata/golden"

// TestGolden runs explain on every example snapshot, as text and as JSON, and
// compares the output with the files in testdata/golden. It covers the
// cluster, all nodes, each node and each namespace that has pods, so a change
// to a rule, a number or the wording shows up as a diff in review.
func TestGolden(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join("..", "..", "examples", "*"))
	if err != nil {
		t.Fatal(err)
	}
	produced := map[string]bool{}
	for _, dir := range dirs {
		snap, err := snapshot.Load(dir)
		if err != nil {
			t.Fatalf("load %s: %v", dir, err)
		}
		if len(snap.Nodes) == 0 {
			continue // not a snapshot: local-demo holds manifests to apply to a demo cluster
		}
		example := filepath.Base(dir)
		for _, args := range goldenCommands(snap) {
			for _, format := range []string{"txt", "json"} {
				path := filepath.Join(goldenDir, example, strings.Join(args, "-")+"."+format)
				produced[path] = true
				t.Run(example+"/"+filepath.Base(path), func(t *testing.T) {
					checkGolden(t, path, explainOutput(t, dir, args, format))
				})
			}
		}
	}
	checkNoStaleGoldens(t, produced)
}

// goldenCommands lists the explain commands recorded for one snapshot: the
// cluster, all nodes, each node and each namespace that has pods.
func goldenCommands(s *snapshot.Snapshot) [][]string {
	cmds := [][]string{{"cluster"}, {"nodes"}}
	for _, n := range s.Nodes {
		cmds = append(cmds, []string{"node", n.Metadata.Name})
	}
	seen := map[string]bool{}
	var namespaces []string
	for _, p := range s.Pods {
		if ns := p.Metadata.Namespace; !seen[ns] {
			seen[ns] = true
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		cmds = append(cmds, []string{"team", ns})
	}
	return cmds
}

// explainOutput runs `kubeshrink explain <args> -f <dir>` and returns what it
// printed.
func explainOutput(t *testing.T, dir string, args []string, format string) []byte {
	t.Helper()
	argv := append([]string{"explain"}, args...)
	argv = append(argv, "-f", dir)
	if format == "json" {
		argv = append(argv, "-o", "json")
	}
	var out, errOut bytes.Buffer
	if code := run(argv, &out, &errOut, noLive); code != 0 {
		t.Fatalf("kubeshrink %s: exit code %d: %s", strings.Join(argv, " "), code, errOut.String())
	}
	return out.Bytes()
}

// checkGolden compares got with the golden file at path, or rewrites the file
// when -update is set.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: run `make goldens` to create it", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s at %s\nIf the change is intended, run `make goldens` and review the diff.", path, firstDifference(string(want), string(got)))
	}
}

// firstDifference describes the first line where want and got differ.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want: %q\n  got:  %q", i+1, wl, gl)
		}
	}
	return "the end of the file"
}

// checkNoStaleGoldens reports golden files that no case produces any more, or
// removes them when -update is set.
func checkNoStaleGoldens(t *testing.T, produced map[string]bool) {
	t.Helper()
	err := filepath.WalkDir(goldenDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || produced[path] {
			return err
		}
		if *update {
			return os.Remove(path)
		}
		t.Errorf("%s is not produced by any case: run `make goldens` to remove it", path)
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}
