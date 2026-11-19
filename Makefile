.DEFAULT_GOAL := help

# MODULES are the repository's Go modules: the root and its nested modules.
MODULES := . codesearch mcpserve cmd/agentkit-tools examples/indexedsearch examples/toolserver
DOC_DIR ?= docs/generated

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*##"}; {printf "  %-16s %s\n", $$1, $$2}'

# internal/policy builds and vets every module for four targets and the host,
# which can take longer than go test's default 10-minute package timeout on a
# loaded machine.
.PHONY: test
test: ## Run all Go tests (root module and the nested modules)
	@rc=0; for m in $(MODULES); do echo "go test ./... ($$m)"; (cd $$m && go test ./... -count=1 -timeout 30m) || rc=1; done; exit $$rc

.PHONY: test-short
test-short: ## Run all Go tests with -short, skipping the slow gates
	@rc=0; for m in $(MODULES); do echo "go test -short ./... ($$m)"; (cd $$m && go test -short ./... -count=1) || rc=1; done; exit $$rc

.PHONY: build
build: ## Build all packages of the root module and the nested modules
	@rc=0; for m in $(MODULES); do echo "go build ./... ($$m)"; (cd $$m && go build ./...) || rc=1; done; exit $$rc
	rm -f examples/indexedsearch/indexedsearch

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
# cgo build) are skipped. Run check-release before a release or a merge.
.PHONY: check
check: fmt vet lint test-short ## Run fmt, vet, lint and the short tests - the fast check

.PHONY: check-release
check-release: fmt vet lint test ## Run fmt, vet, lint and the full tests, slow gates included - use before a release

.PHONY: doc
doc: ## Generate Go package reference (go doc -all) as text files under docs/generated
	@rm -rf $(DOC_DIR)
	@mkdir -p $(DOC_DIR)
	@for m in . codesearch mcpserve cmd/agentkit-tools; do \
		( cd $$m && mod=$$(go list -m) && pkgs=$$(go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./...) && \
		for p in $$(printf '%s\n' "$$pkgs" | grep -v -e /internal/ -e /examples/); do \
			rel=$${p#$$mod}; rel=$${rel#/}; pre=$$m/; [ "$$m" = . ] && pre=; \
			out="$(CURDIR)/$(DOC_DIR)/$$pre$${rel:-$$(basename $$mod)}.txt"; \
			mkdir -p "$$(dirname "$$out")" && go doc -all "$$p" > "$$out" || exit 1; \
		done ) || exit 1; \
	done
	@echo "package docs written to $(DOC_DIR)"

.PHONY: tidy
tidy: ## go mod tidy the root module and the nested modules
	@set -e; for m in $(MODULES); do (cd $$m && go mod tidy); done

# ---------------------------------------------------------------------------
# Repository-specific targets
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build output and the coverage profile
	rm -rf build coverage.txt

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