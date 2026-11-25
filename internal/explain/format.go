// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package explain

import (
	"fmt"
	"strings"

	"github.com/atiasadir/kubeshrink/internal/capacity"
	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// cpuStr renders millicores as cores with one decimal, or as "250m" below one core.
func cpuStr(m float64) string {
	if m < 1000 {
		return fmt.Sprintf("%.0fm", m)
	}
	return fmt.Sprintf("%.1f", m/1000)
}

// memStr renders bytes as Gi with one decimal, or as Mi below 1Gi.
func memStr(b float64) string {
	const mi, gi = 1 << 20, 1 << 30
	if b < gi {
		return fmt.Sprintf("%.0fMi", b/mi)
	}
	return fmt.Sprintf("%.1fGi", b/gi)
}

func fmtAmount(a capacity.Amount) string {
	return cpuStr(a.MilliCPU) + " CPU, " + memStr(a.MemoryBytes) + " memory"
}

func amountOf(r snapshot.Resources) capacity.Amount {
	return capacity.Amount{MilliCPU: float64(r.MilliCPU), MemoryBytes: float64(r.MemoryBytes)}
}

func pct(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

// subjectNamespace returns the namespace of a reference such as
// "Pod/shop/web-1"; cluster-scoped references such as "Node/a" have none.
func subjectNamespace(ref string) (string, bool) {
	parts := strings.Split(ref, "/")
	if len(parts) == 3 {
		return parts[1], true
	}
	return "", false
}

// domainName names a topology key in plain words.
func domainName(key string) string {
	switch key {
	case "kubernetes.io/hostname":
		return "node"
	case "topology.kubernetes.io/zone":
		return "zone"
	case "topology.kubernetes.io/region":
		return "region"
	}
	return key + " value"
}

func dedupe(fs []Finding) []Finding {
	seen := map[string]bool{}
	out := fs[:0]
	for _, f := range fs {
		k := f.Rule + "|" + f.Subject + "|" + f.Claim
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, f)
	}
	return out
}
