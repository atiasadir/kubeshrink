// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

// Package capacity turns pod requests and node capacity into the numbers
// behind bin-packing questions: how full each node is on paper, how each
// node's idle remainder is shared between namespaces, and how few nodes the
// requests would need if placement rules did not exist.
package capacity

import (
	"fmt"
	"math"
	"sort"

	"github.com/atiasadir/kubeshrink/internal/snapshot"
)

// PoolLabels are checked in order to group nodes into pools.
var PoolLabels = []string{
	"karpenter.sh/nodepool",
	"cloud.google.com/gke-nodepool",
	"eks.amazonaws.com/nodegroup",
	"agentpool",
	"kops.k8s.io/instancegroup",
}

// NoPool names the group of nodes that carry none of the PoolLabels.
const NoPool = "(no pool label)"

// PoolOf returns the node's pool and the label it was read from.
func PoolOf(n *snapshot.Node) (pool, label string) {
	for _, l := range PoolLabels {
		if v := n.Metadata.Labels[l]; v != "" {
			return v, l
		}
	}
	return NoPool, ""
}

// Amount is CPU and memory, possibly fractional once idle capacity is shared out.
type Amount struct {
	MilliCPU    float64 `json:"milliCPU"`
	MemoryBytes float64 `json:"memoryBytes"`
}

func amountOf(r snapshot.Resources) Amount {
	return Amount{float64(r.MilliCPU), float64(r.MemoryBytes)}
}

// Add returns the sum of a and b.
func (a Amount) Add(b Amount) Amount {
	return Amount{a.MilliCPU + b.MilliCPU, a.MemoryBytes + b.MemoryBytes}
}

// Sub returns a minus b.
func (a Amount) Sub(b Amount) Amount {
	return Amount{a.MilliCPU - b.MilliCPU, a.MemoryBytes - b.MemoryBytes}
}

// IsZero reports whether both dimensions are zero.
func (a Amount) IsZero() bool { return a.MilliCPU == 0 && a.MemoryBytes == 0 }

func (a Amount) String() string {
	return fmt.Sprintf("cpu=%s memory=%s", snapshot.FormatCPU(int64(math.Round(a.MilliCPU))), snapshot.FormatMemory(int64(math.Round(a.MemoryBytes))))
}

// Ratio returns part divided by whole for each dimension, or 0 where whole is 0.
func Ratio(part, whole Amount) (cpu, memory float64) {
	if whole.MilliCPU > 0 {
		cpu = part.MilliCPU / whole.MilliCPU
	}
	if whole.MemoryBytes > 0 {
		memory = part.MemoryBytes / whole.MemoryBytes
	}
	return cpu, memory
}

// NodeUsage is one node on paper: what it offers and what its pods request.
type NodeUsage struct {
	Node            string            `json:"node"`
	Pool            string            `json:"pool"`
	PoolLabel       string            `json:"poolLabel,omitempty"`
	Schedulable     bool              `json:"schedulable"`
	Allocatable     Amount            `json:"allocatable"`
	AllocatablePods int64             `json:"allocatablePods"`
	Requested       Amount            `json:"requested"`
	NodeBound       Amount            `json:"nodeBound"`
	Idle            Amount            `json:"idle"`
	WorkloadPods    int               `json:"workloadPods"`
	NodeBoundPods   int               `json:"nodeBoundPods"`
	Namespaces      map[string]Amount `json:"namespaces"`
	tenants         map[string]bool
}

// Packing is requested divided by allocatable, per dimension.
func (u *NodeUsage) Packing() (cpu, memory float64) { return Ratio(u.Requested, u.Allocatable) }

// Share is one namespace's slice of one node.
type Share struct {
	Node      string `json:"node"`
	Requested Amount `json:"requested"`
	Idle      Amount `json:"idle"`
	// Exclusive means no other namespace runs workload pods (anything but
	// DaemonSet and static pods) on the node.
	Exclusive bool `json:"exclusive"`
}

// NamespaceUsage totals one namespace across the nodes it runs on.
type NamespaceUsage struct {
	Namespace string  `json:"namespace"`
	Pods      int     `json:"pods"`
	Requested Amount  `json:"requested"`
	Idle      Amount  `json:"idle"`
	Shares    []Share `json:"shares"`
}

// Allocated is what the namespace effectively holds: its requests plus its
// share of the idle capacity on the nodes it uses.
func (u *NamespaceUsage) Allocated() Amount { return u.Requested.Add(u.Idle) }

// Packing is requested divided by allocated, per dimension.
func (u *NamespaceUsage) Packing() (cpu, memory float64) { return Ratio(u.Requested, u.Allocated()) }

// Attribution is the capacity picture of a whole snapshot.
type Attribution struct {
	Nodes      []NodeUsage                `json:"nodes"`
	Namespaces map[string]*NamespaceUsage `json:"namespaces"`
	// Unattributed is idle capacity on nodes where nothing requests that
	// resource, so no namespace can be charged for it.
	Unattributed Amount `json:"unattributed"`
}

// Attribute measures every node and splits each node's idle capacity among
// the namespaces with pods on it, in proportion to their requests, separately
// for CPU and memory. Pods that request nothing are charged nothing.
func Attribute(s *snapshot.Snapshot) *Attribution { return AttributeNodes(s, nil) }

// AttributeNodes is Attribute over the nodes keep accepts, or over every node
// when keep is nil. The pods on the other nodes are left out with them.
func AttributeNodes(s *snapshot.Snapshot, keep func(*snapshot.Node) bool) *Attribution {
	a := &Attribution{Namespaces: map[string]*NamespaceUsage{}}
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if keep != nil && !keep(n) {
			continue
		}
		alloc := snapshot.NodeAllocatable(n)
		pool, label := PoolOf(n)
		u := NodeUsage{
			Node: n.Metadata.Name, Pool: pool, PoolLabel: label, Schedulable: !n.Spec.Unschedulable,
			Allocatable: amountOf(alloc), AllocatablePods: alloc.Pods,
			Namespaces: map[string]Amount{}, tenants: map[string]bool{},
		}
		for _, p := range s.PodsOnNode(n.Metadata.Name) {
			req := amountOf(snapshot.PodRequests(p))
			ns := p.Metadata.Namespace
			u.Requested = u.Requested.Add(req)
			u.Namespaces[ns] = u.Namespaces[ns].Add(req)
			a.namespace(ns).Pods++
			if snapshot.IsNodeBound(p) {
				u.NodeBound = u.NodeBound.Add(req)
				u.NodeBoundPods++
			} else {
				u.WorkloadPods++
				u.tenants[ns] = true
			}
		}
		u.Idle = Amount{math.Max(0, u.Allocatable.MilliCPU-u.Requested.MilliCPU), math.Max(0, u.Allocatable.MemoryBytes-u.Requested.MemoryBytes)}
		a.share(&u)
		a.Nodes = append(a.Nodes, u)
	}
	return a
}

func (a *Attribution) namespace(ns string) *NamespaceUsage {
	u, ok := a.Namespaces[ns]
	if !ok {
		u = &NamespaceUsage{Namespace: ns}
		a.Namespaces[ns] = u
	}
	return u
}

// share splits one node's idle capacity among the namespaces on it.
func (a *Attribution) share(u *NodeUsage) {
	if u.Requested.MilliCPU == 0 {
		a.Unattributed.MilliCPU += u.Idle.MilliCPU
	}
	if u.Requested.MemoryBytes == 0 {
		a.Unattributed.MemoryBytes += u.Idle.MemoryBytes
	}
	names := make([]string, 0, len(u.Namespaces))
	for ns := range u.Namespaces {
		names = append(names, ns)
	}
	sort.Strings(names)
	for _, ns := range names {
		req := u.Namespaces[ns]
		var idle Amount
		if u.Requested.MilliCPU > 0 {
			idle.MilliCPU = u.Idle.MilliCPU * req.MilliCPU / u.Requested.MilliCPU
		}
		if u.Requested.MemoryBytes > 0 {
			idle.MemoryBytes = u.Idle.MemoryBytes * req.MemoryBytes / u.Requested.MemoryBytes
		}
		nsu := a.namespace(ns)
		nsu.Requested = nsu.Requested.Add(req)
		nsu.Idle = nsu.Idle.Add(idle)
		nsu.Shares = append(nsu.Shares, Share{Node: u.Node, Requested: req, Idle: idle, Exclusive: u.tenants[ns] && len(u.tenants) == 1})
	}
}

// Node returns the usage of one node.
func (a *Attribution) Node(name string) *NodeUsage {
	for i := range a.Nodes {
		if a.Nodes[i].Node == name {
			return &a.Nodes[i]
		}
	}
	return nil
}

// Totals sums allocatable and requested capacity over every node.
func (a *Attribution) Totals() (allocatable, requested Amount) {
	for _, n := range a.Nodes {
		allocatable = allocatable.Add(n.Allocatable)
		requested = requested.Add(n.Requested)
	}
	return allocatable, requested
}

// NamespacesByIdle returns the namespaces that hold the most idle capacity
// first: CPU, then memory, then name.
func (a *Attribution) NamespacesByIdle() []*NamespaceUsage {
	out := make([]*NamespaceUsage, 0, len(a.Namespaces))
	for _, u := range a.Namespaces {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.Idle.MilliCPU != y.Idle.MilliCPU {
			return x.Idle.MilliCPU > y.Idle.MilliCPU
		}
		if x.Idle.MemoryBytes != y.Idle.MemoryBytes {
			return x.Idle.MemoryBytes > y.Idle.MemoryBytes
		}
		return x.Namespace < y.Namespace
	})
	return out
}

// PoolUsage summarizes one pool of nodes.
type PoolUsage struct {
	Pool        string `json:"pool"`
	Label       string `json:"label,omitempty"`
	Nodes       int    `json:"nodes"`
	EmptyNodes  int    `json:"emptyNodes"`
	Allocatable Amount `json:"allocatable"`
	Requested   Amount `json:"requested"`
	// MinNodes is how many of the pool's nodes would hold its workload pods
	// if they were packed by requests alone, largest nodes first, keeping each
	// node's DaemonSet and static pods. Placement rules are ignored, so this
	// is a lower bound, not a plan.
	MinNodes int `json:"minNodes"`
}

// Pools groups nodes by pool, in pool name order.
func (a *Attribution) Pools() []PoolUsage {
	groups := map[string][]*NodeUsage{}
	var names []string
	for i := range a.Nodes {
		n := &a.Nodes[i]
		if _, ok := groups[n.Pool]; !ok {
			names = append(names, n.Pool)
		}
		groups[n.Pool] = append(groups[n.Pool], n)
	}
	sort.Strings(names)
	out := make([]PoolUsage, 0, len(names))
	for _, name := range names {
		nodes := groups[name]
		p := PoolUsage{Pool: name, Label: nodes[0].PoolLabel, Nodes: len(nodes), MinNodes: minNodes(nodes)}
		for _, n := range nodes {
			p.Allocatable = p.Allocatable.Add(n.Allocatable)
			p.Requested = p.Requested.Add(n.Requested)
			if n.WorkloadPods == 0 {
				p.EmptyNodes++
			}
		}
		out = append(out, p)
	}
	return out
}

func minNodes(nodes []*NodeUsage) int {
	type room struct {
		cpu, mem float64
		pods     int64
	}
	var need room
	rooms := make([]room, 0, len(nodes))
	var maxCPU, maxMem float64
	for _, n := range nodes {
		need.cpu += n.Requested.MilliCPU - n.NodeBound.MilliCPU
		need.mem += n.Requested.MemoryBytes - n.NodeBound.MemoryBytes
		need.pods += int64(n.WorkloadPods)
		r := room{n.Allocatable.MilliCPU - n.NodeBound.MilliCPU, n.Allocatable.MemoryBytes - n.NodeBound.MemoryBytes, n.AllocatablePods - int64(n.NodeBoundPods)}
		rooms = append(rooms, r)
		maxCPU, maxMem = math.Max(maxCPU, r.cpu), math.Max(maxMem, r.mem)
	}
	if need.pods == 0 {
		return 0
	}
	score := func(r room) float64 {
		s := 0.0
		if maxCPU > 0 {
			s += r.cpu / maxCPU
		}
		if maxMem > 0 {
			s += r.mem / maxMem
		}
		return s
	}
	sort.Slice(rooms, func(i, j int) bool { return score(rooms[i]) > score(rooms[j]) })
	var have room
	for i, r := range rooms {
		have.cpu, have.mem, have.pods = have.cpu+r.cpu, have.mem+r.mem, have.pods+r.pods
		if have.cpu >= need.cpu && have.mem >= need.mem && have.pods >= need.pods {
			return i + 1
		}
	}
	return len(rooms)
}
