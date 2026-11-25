# SPDX-License-Identifier: Apache-2.0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
IMAGE   ?= kubeshrink:dev

.PHONY: all build plugin test lint vuln fmt example chart image clean

all: lint test build

build: ## Build bin/kubeshrink with version info
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kubeshrink ./cmd/kubeshrink

plugin: build ## Also produce bin/kubectl-shrink so `kubectl shrink` works when it is on PATH
	cp bin/kubeshrink bin/kubectl-shrink

test: ## Run unit tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint (https://golangci-lint.run)
	golangci-lint run

vuln: ## Check dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt:
	gofmt -s -w .

example: build ## Explain the synthetic weekend example
	./bin/kubeshrink explain nodes -f examples/weekend-scale-down

chart: ## Lint the Helm chart and render it with each monitor
	helm lint --strict charts/kubeshrink
	helm template kubeshrink charts/kubeshrink > /dev/null
	helm template kubeshrink charts/kubeshrink --set serviceMonitor.enabled=true > /dev/null
	helm template kubeshrink charts/kubeshrink --set podMonitor.enabled=true > /dev/null

image: ## Build the container image locally; nothing is pushed
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) -t $(IMAGE) .

clean:
	rm -rf bin dist
