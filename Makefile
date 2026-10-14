.DEFAULT_GOAL := help
DOC_DIR ?= build/docs
# MODULES are the repository's Go modules: the root and its nested modules.
MODULES := . codesearch sandbox sandbox/gatewayd examples/sandbox

# The sandbox images (sandbox/images/Containerfile). CONTAINER is podman when
# it is on PATH, else docker; SANDBOX_PLATFORM empty means the host platform.
CONTAINER ?= $(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker)
SANDBOX_BASE_IMAGE ?= ghcr.io/agent-fox-dev/agentkit-sandbox
SANDBOX_OVERLAY_IMAGE ?= ghcr.io/agent-fox-dev/agentkit-sandboxd
SANDBOX_IMAGE_TAG ?= dev
SANDBOX_PLATFORM ?=
AGENTKIT_VERSION ?=
AGENTKIT_REVISION ?= $(shell git rev-parse HEAD 2>/dev/null)

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

.PHONY: sandbox-images
sandbox-images: ## Build the sandbox base and overlay images for the host platform
	@command -v "$(CONTAINER)" >/dev/null 2>&1 || { echo "sandbox-images: container engine '$(CONTAINER)' not found on PATH; install podman or docker, or set CONTAINER=<engine>" >&2; exit 1; }
	$(CONTAINER) build -f sandbox/images/Containerfile --target base $(if $(SANDBOX_PLATFORM),--platform $(SANDBOX_PLATFORM) )--build-arg AGENTKIT_VERSION=$(AGENTKIT_VERSION) --build-arg AGENTKIT_REVISION=$(AGENTKIT_REVISION) $(SANDBOX_LABELS) -t $(SANDBOX_BASE_IMAGE):$(SANDBOX_IMAGE_TAG) .
	$(CONTAINER) build -f sandbox/images/Containerfile --target overlay $(if $(SANDBOX_PLATFORM),--platform $(SANDBOX_PLATFORM) )--build-arg AGENTKIT_VERSION=$(AGENTKIT_VERSION) --build-arg AGENTKIT_REVISION=$(AGENTKIT_REVISION) $(SANDBOX_LABELS) -t $(SANDBOX_OVERLAY_IMAGE):$(SANDBOX_IMAGE_TAG) .

# SANDBOX_LABELS restates the version and revision labels on the command
# line: buildah's layer cache keeps a stale LABEL in the overlay stage, which
# has no RUN to carry the build arguments into its cache key.
SANDBOX_LABELS = --label org.opencontainers.image.version=$(AGENTKIT_VERSION) --label org.opencontainers.image.revision=$(AGENTKIT_REVISION)

# The image tests live in the nested sandbox module, so they run there.
.PHONY: sandbox-images-test
sandbox-images-test: sandbox-images ## Build the sandbox images and run the end-to-end image tests against them
	cd sandbox && AGENTKIT_SANDBOX_BASE_IMAGE=$(SANDBOX_BASE_IMAGE):$(SANDBOX_IMAGE_TAG) AGENTKIT_SANDBOX_OVERLAY_IMAGE=$(SANDBOX_OVERLAY_IMAGE):$(SANDBOX_IMAGE_TAG) AGENTKIT_SANDBOX_IMAGE_VERSION=$(AGENTKIT_VERSION) AGENTKIT_CONTAINER=$(CONTAINER) go test -count=1 -v ./images/

# The podman provider's integration tests run against the locally built
# images, in the nested sandbox module. Not part of check: they need an engine.
.PHONY: sandbox-podman-test
sandbox-podman-test: sandbox-images ## Build the sandbox images and run the podman provider's integration tests
	cd sandbox && AGENTKIT_SANDBOX_BASE_IMAGE=$(SANDBOX_BASE_IMAGE):$(SANDBOX_IMAGE_TAG) AGENTKIT_SANDBOX_OVERLAY_IMAGE=$(SANDBOX_OVERLAY_IMAGE):$(SANDBOX_IMAGE_TAG) AGENTKIT_SANDBOX_IMAGE_VERSION=$(AGENTKIT_VERSION) AGENTKIT_CONTAINER=$(CONTAINER) go test -count=1 -v ./podman/

# The landlock provider's tests, verbose, with the opt-in public clone; its
# offline and kernel-gated tests already run in check.
AGENTKIT_SANDBOX_GIT_URL ?= https://github.com/agent-fox-dev/agentkit-go
.PHONY: sandbox-landlock-test
sandbox-landlock-test: ## Run the landlock provider's tests verbosely, including the public GitRepo clone
	cd sandbox && AGENTKIT_SANDBOX_GIT_URL=$(AGENTKIT_SANDBOX_GIT_URL) go test -count=1 -v ./landlock/

.PHONY: check
check: fmt vet lint test ## Run fmt, vet, lint and test - use before committing

.PHONY: clean-branches
clean-branches:
	@git branch --list 'feature/*' | xargs -r git branch -D
	@git branch --list 'fix/*' | xargs -r git branch -D
	@git branch --list 'impl/*' | xargs -r git branch -D
	@git branch --list 'docs/*' | xargs -r git branch -D
	@git branch --list 'worktree-agent-*' | xargs -r git branch -D