.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*##"}; {printf "  %-16s %s\n", $$1, $$2}'

.PHONY: test
test: ## Run all Go tests (root module + difftest, codesearch and examples/codesearch submodules)
	go test ./...
	cd difftest && go test ./...
	cd codesearch && go test ./...
	cd examples/codesearch && go test ./...

.PHONY: build-examples
build-examples: ## Install examples/triage, examples/cleaner and examples/flatline into ./bin
	go install ./examples/triage
	go install ./examples/cleaner

.PHONY: fmt
fmt: ## gofmt all source files in place
	gofmt -l -w .

.PHONY: vet
vet: ## go vet the root module and difftest, codesearch and examples/codesearch submodules
	go vet ./...
	cd difftest && go vet ./...
	cd codesearch && go vet ./...
	cd examples/codesearch && go vet ./...

.PHONY: lint
lint: ## Run golangci-lint if installed, otherwise skip
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./... && (cd difftest && golangci-lint run ./...) && (cd codesearch && golangci-lint run ./...) && (cd examples/codesearch && golangci-lint run ./...); \
	else \
		echo "golangci-lint not installed; skipping (see https://golangci-lint.run)"; \
	fi

.PHONY: tidy
tidy: ## go mod tidy the root module and submodules
	go mod tidy
	cd difftest && go mod tidy
	cd codesearch && go mod tidy
	cd examples/codesearch && go mod tidy
	@if [ -d ../spec/golang ]; then cd examples/flatline && go mod tidy; fi

.PHONY: check
check: fmt vet lint test ## Run fmt, vet, lint and test - use before committing

