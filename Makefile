.DEFAULT_GOAL := help

# MODULES are the repository's Go modules: the root and its nested modules.
MODULES := . codesearch sandbox sandbox/gatewayd examples/sandbox
DOC_DIR ?= build/docs

VERSION    := $(shell git describe --tags 2>/dev/null || echo "0.1.0")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "\
  -X github.com/agent-fox-dev/agentfox.Version=$(VERSION) \
  -X github.com/agent-fox-dev/agentfox.Commit=$(COMMIT) \
  -X github.com/agent-fox-dev/agentfox.BuildTime=$(BUILD_TIME)"

CONTAINER_REGISTRY ?= quay.io/agentfox

SANDBOX_IMAGE ?= agentkit-sandbox
SANDBOX_IMAGE_TAG ?= $(VERSION)
SANDBOXD_IMAGE ?= agentkit-sandboxd
SANDBOXD_IMAGE_TAG ?= $(VERSION)

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*##"}; {printf "  %-16s %s\n", $$1, $$2}'

.PHONY: test
test: ## Run all Go tests (root module and the nested modules)
	@rc=0; for m in $(MODULES); do echo "go test ./... ($$m)"; (cd $$m && go test ./...) || rc=1; done; exit $$rc

.PHONY: coverage
coverage:
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

.PHONY: check
check: fmt vet lint test ## Run fmt, vet, lint and test - use before committing

.PHONY: doc
doc: ## Generate Go package reference (go doc -all) as text files under build/doc
	@rm -rf $(DOC_DIR)
	@mkdir -p $(DOC_DIR)
	@for m in . codesearch sandbox sandbox/gatewayd; do \
		( cd $$m && mod=$$(go list -m) && pkgs=$$(go list ./...) && \
		for p in $$(printf '%s\n' "$$pkgs" | grep -v -e /internal/ -e /examples/); do \
			rel=$${p#$$mod}; rel=$${rel#/}; out="$(CURDIR)/$(DOC_DIR)/$${rel:-$$(basename $$mod)}.txt"; \
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

.PHONY: build-containers
build-containers: build-sandbox-container build-sandboxd-container

.PHONY: build-sandbox-container
build-sandbox-container:
	podman build \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):$(SANDBOX_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOX_IMAGE):latest \
		-f containers/agentkit-sandbox/Containerfile .

.PHONY: build-sandboxd-container
build-sandboxd-container:
	podman build \
		--build-arg VERSION=$(VERSION) \
		--build-arg BUILD=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOXD_IMAGE):$(SANDBOXD_IMAGE_TAG) \
		-t $(CONTAINER_REGISTRY)/$(SANDBOXD_IMAGE):latest \
		-f containers/agentkit-sandboxd/Containerfile .

.PHONY: clean-branches
clean-branches:
	@git branch --list 'feature/*' | xargs -r git branch -D
	@git branch --list 'fix/*' | xargs -r git branch -D
	@git branch --list 'impl/*' | xargs -r git branch -D
	@git branch --list 'docs/*' | xargs -r git branch -D
	@git branch --list 'worktree-agent-*' | xargs -r git branch -D