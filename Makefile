# SPDX-License-Identifier: Apache-2.0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
IMAGE   ?= kubeshrink:dev
# Trivy runs from its official image, pinned by digest (bump it by hand;
# Dependabot does not read Makefiles). Its vulnerability database is cached in
# a Docker volume and refreshed when a newer one is published.
TRIVY_IMAGE ?= aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb
TRIVY_RUN   := docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v kubeshrink-trivy-cache:/root/.cache/trivy

.PHONY: all build plugin test lint vuln fmt example chart image clean

all: lint test build

build: ## Build bin/kubeshrink with version info
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kubeshrink ./cmd/kubeshrink

plugin: build ## Also produce bin/kubectl-shrink so `kubectl shrink` works when it is on PATH
	cp bin/kubeshrink bin/kubectl-shrink

test: ## Run unit tests with the race detector
	go test -race ./...

.PHONY: goldens
goldens: ## Rewrite the golden files after an intended output change, then review the diff
	go test ./cmd/kubeshrink -run TestGolden -update

lint: ## Run golangci-lint (https://golangci-lint.run)
	golangci-lint run

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt:
	gofmt -s -w .

example: build ## Explain the synthetic weekend example
	./bin/kubeshrink explain nodes -f examples/weekend-scale-down

# Any well-formed digest, to check how the chart renders a pinned image.
CHART_TEST_DIGEST := sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef

chart: ## Lint the Helm chart, render it with each monitor and check the image reference
	helm lint --strict charts/kubeshrink
	helm template kubeshrink charts/kubeshrink > /dev/null
	helm template kubeshrink charts/kubeshrink --set serviceMonitor.enabled=true > /dev/null
	helm template kubeshrink charts/kubeshrink --set podMonitor.enabled=true > /dev/null
	helm template kubeshrink charts/kubeshrink | grep -q 'image: "kubeshrink:dev"'
	helm template kubeshrink charts/kubeshrink --set image.digest=$(CHART_TEST_DIGEST) | grep -q 'image: "kubeshrink@$(CHART_TEST_DIGEST)"'
	helm template kubeshrink charts/kubeshrink --set image.tag=v1 --set image.digest=$(CHART_TEST_DIGEST) | grep -q 'image: "kubeshrink:v1@$(CHART_TEST_DIGEST)"'
	! helm template kubeshrink charts/kubeshrink --set image.digest=latest > /dev/null 2>&1

image: ## Build the container image locally; nothing is pushed
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE) .

.PHONY: scan sbom
scan: ## Scan the local image: list every finding, fail on fixable HIGH or CRITICAL ones
	$(TRIVY_RUN) $(TRIVY_IMAGE) image --no-progress $(IMAGE)
	$(TRIVY_RUN) $(TRIVY_IMAGE) image --no-progress --quiet --exit-code 1 --severity HIGH,CRITICAL --ignore-unfixed $(IMAGE)

sbom: ## Write a CycloneDX SBOM of the local image to dist/kubeshrink.cdx.json
	mkdir -p dist
	$(TRIVY_RUN) -v $(CURDIR)/dist:/out $(TRIVY_IMAGE) image --no-progress --quiet --format cyclonedx --output /out/kubeshrink.cdx.json $(IMAGE)

# End-to-end tests run on a throwaway kind cluster that exists only for the run.
KIND           ?= go run sigs.k8s.io/kind@v0.33.0
E2E_CLUSTER    ?= kubeshrink-e2e
E2E_NODE_IMAGE ?= kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0

.PHONY: e2e
e2e: ## Test the CLI and the chart on a throwaway kind cluster (needs Docker, kubectl and helm)
	$(MAKE) image IMAGE=kubeshrink:e2e
	KIND="$(KIND)" E2E_CLUSTER=$(E2E_CLUSTER) E2E_NODE_IMAGE=$(E2E_NODE_IMAGE) test/e2e/run.sh

clean:
	rm -rf bin dist
