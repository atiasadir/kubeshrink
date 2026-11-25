// SPDX-License-Identifier: Apache-2.0
// Copyright The KubeShrink Authors.

package snapshot

import "testing"

func TestControlPlaneLabel(t *testing.T) {
	for _, c := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{"node-role.kubernetes.io/control-plane": ""}, "node-role.kubernetes.io/control-plane"},
		{map[string]string{"node-role.kubernetes.io/master": "true"}, "node-role.kubernetes.io/master"},
		{map[string]string{"node-role.kubernetes.io/master": "", "node-role.kubernetes.io/control-plane": ""}, "node-role.kubernetes.io/control-plane"},
		{map[string]string{"node-role.kubernetes.io/worker": ""}, ""},
		{nil, ""},
	} {
		n := &Node{Metadata: ObjectMeta{Labels: c.labels}}
		if got := ControlPlaneLabel(n); got != c.want {
			t.Errorf("ControlPlaneLabel(%v) = %q, want %q", c.labels, got, c.want)
		}
		if got := IsControlPlane(n); got != (c.want != "") {
			t.Errorf("IsControlPlane(%v) = %v", c.labels, got)
		}
	}
}
