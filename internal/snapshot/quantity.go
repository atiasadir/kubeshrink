// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var quantitySuffixes = map[string]float64{
	"n": 1e-9, "u": 1e-6, "m": 1e-3, "": 1,
	"k": 1e3, "M": 1e6, "G": 1e9, "T": 1e12, "P": 1e15, "E": 1e18,
	"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40, "Pi": 1 << 50, "Ei": 1 << 60,
}

// ParseQuantity parses the common Kubernetes quantity forms ("250m", "2",
// "512Mi", "1.5Gi", "1e3"). It is intentionally simpler than
// k8s.io/apimachinery's resource.Quantity.
func ParseQuantity(s string) (float64, error) {
	s = strings.TrimSpace(s)
	i := len(s)
	for i > 0 && unicode.IsLetter(rune(s[i-1])) {
		i--
	}
	num, suffix := s[:i], s[i:]
	mult, ok := quantitySuffixes[suffix]
	if !ok {
		return 0, fmt.Errorf("unknown quantity suffix %q in %q", suffix, s)
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("invalid quantity %q", s)
	}
	return v * mult, nil
}

// Resources holds the three dimensions the fit heuristic compares.
type Resources struct {
	MilliCPU    int64 `json:"milliCPU"`
	MemoryBytes int64 `json:"memoryBytes"`
	Pods        int64 `json:"pods"`
}

func (r Resources) Add(o Resources) Resources {
	return Resources{r.MilliCPU + o.MilliCPU, r.MemoryBytes + o.MemoryBytes, r.Pods + o.Pods}
}

func (r Resources) Sub(o Resources) Resources {
	return Resources{r.MilliCPU - o.MilliCPU, r.MemoryBytes - o.MemoryBytes, r.Pods - o.Pods}
}

// Max returns the larger value of each dimension.
func (r Resources) Max(o Resources) Resources {
	return Resources{max(r.MilliCPU, o.MilliCPU), max(r.MemoryBytes, o.MemoryBytes), max(r.Pods, o.Pods)}
}

func (r Resources) String() string {
	return fmt.Sprintf("cpu=%s memory=%s", FormatCPU(r.MilliCPU), FormatMemory(r.MemoryBytes))
}

// unlimitedPods is used when a node does not report allocatable pods.
const unlimitedPods = 1 << 30

func NodeAllocatable(n *Node) Resources {
	r := ParseResources(n.Status.Allocatable)
	r.Pods = unlimitedPods
	if v, ok := n.Status.Allocatable["pods"]; ok {
		r.Pods = whole(v)
	}
	return r
}

// ParseResources reads the cpu and memory entries of a resource list such as
// requests, a VPA recommendation or a LimitRange default.
func ParseResources(m map[string]string) Resources {
	var r Resources
	if v, ok := m["cpu"]; ok {
		r.MilliCPU = milli(v)
	}
	if v, ok := m["memory"]; ok {
		r.MemoryBytes = whole(v)
	}
	return r
}

// ContainerRequests returns a container's CPU and memory requests.
func ContainerRequests(c Container) Resources { return ParseResources(c.Resources.Requests) }

// PodRequests returns what the scheduler reserves for a pod: the larger of its
// long-running containers (app containers plus native sidecars) and its
// heaviest init step, plus pod overhead. Every pod also takes one pod slot.
func PodRequests(p *Pod) Resources {
	var running, sidecars, initPeak Resources
	for _, c := range p.Spec.InitContainers {
		r := ContainerRequests(c)
		if c.IsSidecar() {
			sidecars = sidecars.Add(r)
			continue
		}
		initPeak = initPeak.Max(sidecars.Add(r))
	}
	for _, c := range p.Spec.Containers {
		running = running.Add(ContainerRequests(c))
	}
	total := running.Add(sidecars).Max(initPeak).Add(ParseResources(p.Spec.Overhead))
	total.Pods = 1
	return total
}

func milli(v string) int64 {
	q, _ := ParseQuantity(v)
	return int64(math.Round(q * 1000))
}

func whole(v string) int64 {
	q, _ := ParseQuantity(v)
	return int64(math.Round(q))
}

func FormatCPU(m int64) string {
	if m < 0 {
		return "-" + FormatCPU(-m)
	}
	if m%1000 == 0 {
		return strconv.FormatInt(m/1000, 10)
	}
	return fmt.Sprintf("%dm", m)
}

func FormatMemory(b int64) string {
	if b < 0 {
		return "-" + FormatMemory(-b)
	}
	units := []struct {
		suffix string
		size   int64
	}{{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}}
	for _, u := range units {
		if b >= u.size && b%u.size == 0 {
			return fmt.Sprintf("%d%s", b/u.size, u.suffix)
		}
	}
	for _, u := range units {
		if b >= u.size {
			return fmt.Sprintf("%.1f%s", float64(b)/float64(u.size), u.suffix)
		}
	}
	return strconv.FormatInt(b, 10)
}

// FormatResourceList renders a resource list such as "cpu=1, memory=2Gi" in a
// stable order.
func FormatResourceList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}
