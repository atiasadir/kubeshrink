#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Copyright The KubeShrink Authors.
#
# Creates a throwaway kind cluster, loads the kubeshrink:e2e image into it,
# runs the end-to-end tests and deletes the cluster again, also when a step
# fails. The cluster's kubeconfig goes to a temporary file, so ~/.kube/config
# and its current context are never touched. Run it with `make e2e`, which
# builds the image and sets KIND, E2E_CLUSTER and E2E_NODE_IMAGE.
set -euo pipefail

: "${KIND:?run this with make e2e}" "${E2E_CLUSTER:?run this with make e2e}" "${E2E_NODE_IMAGE:?run this with make e2e}"
cd "$(dirname "$0")/../.."

workdir=$(mktemp -d)
kubeconfig="$workdir/kubeconfig"
trap 'rm -rf "$workdir"' EXIT

# KIND may be a command with arguments (go run sigs.k8s.io/kind@...), so it is
# deliberately left unquoted.
if $KIND get clusters 2>/dev/null | grep -qx "$E2E_CLUSTER"; then
  echo "A kind cluster named $E2E_CLUSTER already exists. Delete it, or set E2E_CLUSTER to another name." >&2
  exit 1
fi
$KIND create cluster --name "$E2E_CLUSTER" --image "$E2E_NODE_IMAGE" --kubeconfig "$kubeconfig" --wait 120s
trap '$KIND delete cluster --name "$E2E_CLUSTER" --kubeconfig "$kubeconfig"; rm -rf "$workdir"' EXIT

$KIND load docker-image kubeshrink:e2e --name "$E2E_CLUSTER"
go test -tags e2e -count=1 -v ./test/e2e -args -kubeconfig "$kubeconfig" -context "kind-$E2E_CLUSTER" -image-repository kubeshrink -image-tag e2e
