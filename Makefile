.DEFAULT_GOAL := help

# MODULES are the repository's Go modules: the root and its nested modules.
MODULES := . codesearch sandbox sandbox/gatewayd mcpserve examples/sandbox examples/gateway examples/harness
DOC_DIR ?= docs/generated

VERSION    := $(shell git describe --tags 2>/dev/null || echo "0.1.0")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "\
  -X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=$(VERSION) \
  -X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.commit=$(COMMIT) \
  -X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.buildTime=$(BUILD_TIME)"

CONTAINER_REGISTRY ?= quay.io/agentfox

SANDBOX_IMAGE ?= agentkit-sandbox
SANDBOX_IMAGE_TAG ?= $(VERSION)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*##"}; {printf "  %-16s %s\n", $$1, $$2}'

# internal/policy runs make check, make check-release and the cross-target builds
# as child processes. On a green tree it takes 9 to 11 minutes, depending on the
# machine's load, so go test's default 10-minute package timeout is not enough.
.PHONY: test
test: ## Run all Go tests (root module and the nested modules)
	@rc=0; for m in $(MODULES); do echo "go test ./... ($$m)"; (cd $$m && go test ./... -count=1 -timeout 30m) || rc=1; done; exit $$rc

.PHONY: test-short
test-short: ## Run all Go tests with -short, skipping the slow gates
	@rc=0; for m in $(MODULES); do echo "go test -short ./... ($$m)"; (cd $$m && go test -short ./... -count=1) || rc=1; done; exit $$rc

.PHONY: build
build: ## Build all packages of the root module and the nested modules
	@rc=0; for m in $(MODULES); do echo "go build ./... ($$m)"; (cd $$m && go build ./...) || rc=1; done; exit $$rc
	rm examples/gateway/gateway examples/harness/harness examples/sandbox/sandbox

.PHONY: coverage
coverage: ## Run the root module's tests with a coverage report
	go test ./... -coverprofile=coverage.txt -covermode=atomic
	go tool cover -func=coverage.txt

.PHONY: fmt
fmt: ## gofmt all source files in place
	gofmt -l -w .

.PHONY: vet
vet: ## go vet the root module and the nested modules
	@rc=0; for m in $(MODULES); do echo "go vet ./... ($$m)"; (cd $$m && go vet ./...) || rc=1; done; exit $$rc

.PHONY: lint
lint: ## Run golangci-lint if installed, otherwise skip
	@if command -v golangci-lint >/dev/null 2>&1; then \
		set -e; for m in $(MODULES); do (cd $$m && golangci-lint run ./...); done; \
	else \
		echo "golangci-lint not installed; skipping (see https://golangci-lint.run)"; \
	fi

# check is the fast loop for day-to-day work: it runs the tests with -short, so
# the slow gates in internal/policy (the cross-target build and vet, the host
# cgo build, the nested make runs) are skipped. Run check-release before a
# release or a merge.
.PHONY: check
check: fmt vet lint test-short ## Run fmt, vet, lint and the short tests - the fast check

# AGENTKIT_IN_MAKE_CHECK tells the tests that run inside make check-release to
# skip the checks that would start make check-release again or repeat a suite
# it already runs.
.PHONY: check-release
check-release: export AGENTKIT_IN_MAKE_CHECK := 1
check-release: fmt vet lint test ## Run fmt, vet, lint and the full tests, slow gates included - use before a release

.PHONY: doc
doc: ## Generate Go package reference (go doc -all) as text files under docs/generated
	@rm -rf $(DOC_DIR)
	@mkdir -p $(DOC_DIR)
	@for m in . codesearch sandbox sandbox/gatewayd mcpserve; do \
		( cd $$m && mod=$$(go list -m) && pkgs=$$(go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./...) && \
		for p in $$(printf '%s\n' "$$pkgs" | grep -v -e /internal/ -e /examples/); do \
			rel=$${p#$$mod}; rel=$${rel#/}; pre=$$m/; [ "$$m" = . ] && pre=; \
			out="$(CURDIR)/$(DOC_DIR)/$$pre$${rel:-$$(basename $$mod)}.txt"; \
			mkdir -p "$$(dirname "$$out")" && go doc -all "$$p" > "$$out" || exit 1; \
		done ) || exit 1; \
	done
	@echo "package docs written to $(DOC_DIR)"

PROTO_DIR := sandbox/internal/sandboxpb
PROTOC_GEN_GO := google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
PROTOC_GEN_GO_GRPC := google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

# proto generates into build/.proto (a directory `go build ./...` skips) and
# moves each file into place, so a build running at the same time never reads
# a half-written file. The protoc release in the generated headers is pinned
# to "(unknown)" so the output does not depend on which protoc is installed.
.PHONY: proto
proto: ## Regenerate the sandbox agent and gateway protocols' Go code (needs protoc)
	@command -v protoc >/dev/null 2>&1 || { echo "make proto: protoc not found on PATH; install protoc" >&2; exit 1; }
	GOBIN=$(CURDIR)/build/bin go install $(PROTOC_GEN_GO)
	GOBIN=$(CURDIR)/build/bin go install $(PROTOC_GEN_GO_GRPC)
	@rm -rf build/.proto && mkdir -p build/.proto
	protoc -I $(PROTO_DIR) --plugin=protoc-gen-go=build/bin/protoc-gen-go --plugin=protoc-gen-go-grpc=build/bin/protoc-gen-go-grpc \
		--go_out=build/.proto --go_opt=paths=source_relative \
		--go-grpc_out=build/.proto --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/sandbox.proto $(PROTO_DIR)/gateway.proto
	@for f in sandbox.pb.go sandbox_grpc.pb.go gateway.pb.go gateway_grpc.pb.go; do \
		sed -E 's#^(//[[:space:]-]+protoc[[:space:]]+)v.*$$#\1(unknown)#' build/.proto/$$f > build/.proto/$$f.tmp && \
		mv build/.proto/$$f.tmp $(PROTO_DIR)/$$f || exit 1; \
	done
	@for p in sandbox.proto gateway.proto; do \
		if command -v sha256sum >/dev/null 2>&1; then sha256sum $(PROTO_DIR)/$$p; \
		else shasum -a 256 $(PROTO_DIR)/$$p; fi | cut -d' ' -f1 > build/.proto/$$p.sha256 && \
		mv build/.proto/$$p.sha256 $(PROTO_DIR)/$$p.sha256 || exit 1; \
	done
	@rm -rf build/.proto
	@echo "generated code and hash written to $(PROTO_DIR)"

.PHONY: tidy
tidy: ## go mod tidy the root module and the nested modules
	@set -e; for m in $(MODULES); do (cd $$m && go mod tidy); done

# ---------------------------------------------------------------------------
# Repository-specific targets
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build output and the coverage profile
	rm -rf build coverage.txt

.PHONY: build-containers
build-containers: build-sandbox-container

.PHONY: build-sandbox-container
build-sandbox-container:
	podman build \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):$(SANDBOX_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):latest \
		-f containers/agentkit-sandbox/Containerfile .

# The image variables the opt-in integration tests read: the image
# build-containers tags as latest.
SANDBOX_TEST_ENV = AGENTKIT_SANDBOX_BASE_IMAGE=$(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):latest AGENTKIT_SANDBOX_IMAGE_VERSION=$(VERSION) AGENTKIT_CONTAINER=podman

# The podman provider's integration tests run against the locally built
# image, in the nested sandbox module. Not part of check: they need an engine.
.PHONY: sandbox-podman-test
sandbox-podman-test: build-containers ## Build the sandbox image and run the podman provider's integration tests
	cd sandbox && $(SANDBOX_TEST_ENV) go test -count=1 -v ./podman/

# The landlock provider's tests, verbose, with the opt-in public clone; its
# offline and kernel-gated tests already run in check.
AGENTKIT_SANDBOX_GIT_URL ?= https://github.com/agent-fox-dev/agentkit-go
.PHONY: sandbox-landlock-test
sandbox-landlock-test: ## Run the landlock provider's tests verbosely, including the public GitRepo clone
	cd sandbox && AGENTKIT_SANDBOX_GIT_URL=$(AGENTKIT_SANDBOX_GIT_URL) go test -count=1 -v ./landlock/

# The gateway command lives in its own nested module; AGENTKIT_VERSION, when
# set, stamps the version it reports.
.PHONY: sandbox-gateway
sandbox-gateway: ## Build build/bin/agentkit-gateway (AGENTKIT_VERSION stamps its version)
	cd sandbox/gatewayd && go build -trimpath $(if $(AGENTKIT_VERSION),-ldflags "-X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=$(AGENTKIT_VERSION)" )-o $(CURDIR)/build/bin/agentkit-gateway ./cmd/agentkit-gateway

# The gateway's opt-in Podman tests run against the locally built base image.
# Not part of check: they need an engine.
.PHONY: sandbox-gateway-test
sandbox-gateway-test: build-sandbox-container ## Build the sandbox base image and run the gateway's opt-in Podman tests
	cd sandbox/gatewayd && AGENTKIT_SANDBOX_BASE_IMAGE=$(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):latest AGENTKIT_CONTAINER=podman go test -count=1 -v -run Podman .

# The live provider tests run one real tool loop on each provider whose
# prerequisites are present (an Anthropic credential, OPENROUTER_API_KEY, a
# reachable Ollama server with the tier's model) and spend money on the first
# two. AGENTKIT_LIVE=1 is the opt-in, and neither check nor check-release runs
# them.
.PHONY: provider-live-test
provider-live-test: ## Run the live provider smoke tests: one real tool loop on each provider that is ready; costs money
	AGENTKIT_LIVE=1 go test -count=1 -v -timeout 30m -run TestLive ./internal/providerconformance

.PHONY: clean-branches
clean-branches:
	@git branch --list 'feature/*' | xargs -r git branch -D
	@git branch --list 'fix/*' | xargs -r git branch -D
	@git branch --list 'impl/*' | xargs -r git branch -D
	@git branch --list 'docs/*' | xargs -r git branch -D
	@git branch --list 'worktree-agent-*' | xargs -r git branch -D