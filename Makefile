SHELL := /usr/bin/env bash -o pipefail

BINARY      := scaleway-finops-exporter
PKG         := github.com/SeeMyPing/scaleway-finops-exporter
IMAGE       ?= ghcr.io/seemyping/$(BINARY)
BIN_DIR     := bin
COVER_MIN   ?= 80

VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION    ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BRANCH      ?= $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
BUILD_USER  ?= $(shell whoami 2>/dev/null || echo unknown)

VERSION_PKG := github.com/prometheus/common/version
LDFLAGS     := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Revision=$(REVISION) \
	-X $(VERSION_PKG).Branch=$(BRANCH) \
	-X $(VERSION_PKG).BuildUser=$(BUILD_USER) \
	-X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

# Tool versions, kept in sync with .github/workflows.
GOLANGCI_LINT_VERSION ?= v2.13.2
GOVULNCHECK_VERSION   ?= v1.8.0
GOFUMPT_VERSION       ?= v0.12.0

GOLANGCI_LINT ?= golangci-lint
GOVULNCHECK   ?= govulncheck
PROMTOOL      ?= promtool
KUSTOMIZE     ?= kustomize
KUBECONFORM   ?= kubeconform
CRD_SCHEMAS   := https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: tools
tools: ## Install pinned development tools into GOBIN
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

.PHONY: build
build: ## Build the exporter binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) ./cmd/$(BINARY)

.PHONY: run
run: build ## Build and run the exporter with local credentials
	./$(BIN_DIR)/$(BINARY) $(ARGS)

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test -race ./...

.PHONY: test-integration
test-integration: ## Run the integration tests against the real Scaleway API (needs SCW_* credentials)
	go test -tags integration -run Integration -count=1 -v ./internal/scaleway/

.PHONY: cover
cover: ## Run tests with coverage on internal/ and enforce COVER_MIN
	go test -race -covermode=atomic -coverprofile=coverage.out ./internal/...
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	echo "total coverage: $${total}% (minimum $(COVER_MIN)%)"; \
	awk -v t="$${total}" -v m="$(COVER_MIN)" 'BEGIN { exit (t+0 < m+0) }' || { echo "coverage below threshold"; exit 1; }

.PHONY: fmt
fmt: ## Format sources (gofumpt + goimports through golangci-lint)
	$(GOLANGCI_LINT) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if formatting or go.mod tidiness would change files
	go mod tidy
	$(GOLANGCI_LINT) fmt ./...
	git diff --exit-code -- . ':(exclude)coverage.out'

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: vuln
vuln: ## Check dependencies for known vulnerabilities
	$(GOVULNCHECK) ./...

.PHONY: metrics-check
metrics-check: ## Lint the golden expositions generated from test fixtures with promtool
	go test ./internal/collector
	@for f in internal/collector/testdata/*.prom; do \
		echo "promtool check metrics $$f"; \
		$(PROMTOOL) check metrics < $$f || exit 1; \
	done

.PHONY: rules-test
rules-test: ## Validate and unit test the Prometheus alerting rules
	$(PROMTOOL) check rules deploy/prometheus/rules.yml
	$(PROMTOOL) test rules deploy/prometheus/rules_test.yml

.PHONY: manifests-check
manifests-check: ## Validate the Kubernetes manifests and the Grafana dashboard JSON
	$(KUSTOMIZE) build deploy/kubernetes | $(KUBECONFORM) -strict -summary \
		-schema-location default -schema-location '$(CRD_SCHEMAS)'
	$(KUBECONFORM) -strict -summary deploy/kubernetes/secret.example.yaml
	python3 -m json.tool deploy/grafana/scaleway-finops.json > /dev/null

.PHONY: docker
docker: ## Build the container image for the local platform
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg BRANCH=$(BRANCH) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE):$(VERSION) .

.PHONY: ci
ci: fmt-check lint cover vuln metrics-check rules-test manifests-check build ## Run everything the CI runs (except image build/scan)

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) dist coverage.out coverage.html
