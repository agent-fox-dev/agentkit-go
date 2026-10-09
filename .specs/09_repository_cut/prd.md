---
spec_id: "09"
spec_name: "repository_cut"
title: "Cut AgentKit Repository, Remove Obsolete Packages, and Rename Module"
status: "active"
created_at: "2026-10-09T11:10:14.707403Z"
updated_at: "2026-10-09T11:10:14.707403Z"
intent_hash: "37b3682418de4d991869e934ca72d2e78bb386b77a0b32ef408f7b0b40e1e42d"
schema_version: 2
source: "docs/prd/10-cut-agentkit-down-to-the-hands.md"
---
## Intent

Eliminate unmaintained provider wires, auxiliary subsystems, and dead tool abstractions to reduce AgentKit's footprint down to its workspace mechanics ("the hands") and foundational driver scaffolding, rename the module path to `github.com/agent-fox-dev/agentkit-go`, enforce an automated dependency allowlist gate, and remove flaky wall-clock performance tests.

## Goals

1. Delete unneeded provider backends (`provider/openai`, `provider/openairesponses`, `provider/google`, `provider/ollama`) and shared multi-wire transport/salvage/repair/auth infrastructure, retaining only `provider/anthropic` and `provider/faux`.
2. Delete obsolete auxiliary packages and commands: `plugins` and `cmd/validate-plugins`, `skills` and `_skills/code-review`, `session`, `subagent`, `difftest`, `imagex`, `compaction`, `middleware`, and `stop`.
3. Delete unneeded tools and server files: `tools/fetch.go`, `tools/ssrf.go`, `tools/powershell.go`, `mcp/server.go`, `mcp/serve.go`, and `wire/bind.go`.
4. Delete unneeded root files: `images.go`, `images_test.go`, `skillsconfig.go`, `skillsconfig_test.go`, `audit.go`, `audit_test.go`, `deferred.go`, `deferred_test.go`, `resume.go`, `resume_test.go`, `resume_metadata_test.go`, `core/audit.go`, `core/audit_test.go`, `core/trace.go`, and `core/plugin.go`.
5. Delete unneeded examples: all under `examples/` except `agentdemo`, `codingagent`, `customtools`, `codemode`, and `mcp`. Remove `build-examples` and `difftest` targets from `Makefile`.
6. Update `tools/tools.go` so `read_file` detects image magic bytes directly and returns a descriptive error naming the format, removing dependency on `imagex` and `golang.org/x/image`.
7. Rename the Go module in `go.mod` to `github.com/agent-fox-dev/agentkit-go` and update all import declarations across the repository and `codesearch/go.mod`.
8. Prune removed direct dependencies (`golang.org/x/net`, `golang.org/x/image`, `golang.org/x/time`, `code.dny.dev/ssrf`) from `go.mod` via `go mod tidy`.
9. Add an automated dependency allowlist test in `internal/policy` that fails if any direct dependency outside the approved set appears in `go.mod`.
10. Delete `perf_budget_test.go` and remove duration threshold comparisons from test assertions across `tools/concurrency_test.go` and the remaining suite so tests do not fail on machine load.
11. Ensure `make check` (`go test ./...`, `go vet ./...`, formatting, and linting) succeeds cleanly across the root module and the `codesearch` submodule.

## Non-goals

- Rewriting `provider/anthropic` over `github.com/anthropics/anthropic-sdk-go` (handled in the subsequent scope: `anthropic_wire`).
- Refactoring `agentkit.Config`, `agentkit.New`, prefix cache breakpoint stamping, and tool result `Prune` (handled in the subsequent scope: `minimal_driver`).
- Shrinking `core` vocabulary types, folding `jsonx` into `schema.Parse`, and pure-function `guard.Check` (handled in the subsequent scope: `core_schema_guard`).
- Multi-provider support or keeping compatibility with 0.4.x APIs.
- Retaining interactive agent features (steering queues, resumption, multi-turn history branching).

## Background

AgentKit was originally designed as a general-purpose, dependency-free agent SDK supporting five provider wire APIs, bidirectional MCP, plugins, skills, sessions, compaction, delegation, and differential testing. It grew to over 65,000 lines of production code and 69,000 lines of tests. In practice, its sole consumer (`agent-fox`) uses only Claude models, manages its own sessions and prompt files (`AGENTS.md`, `.specs/steering.md`), and needs only AgentKit's workspace mechanics ("the hands") and a minimal driver loop.

PRD 09 replaced hand-rolled parsers and wire code with libraries (tree-sitter outlines, `encoding/json/v2`, official MCP SDK). PRD 08 added Starlark-based code mode. However, the repository still carries thousands of lines of unused provider adapters (`openai`, `google`, `ollama`), unused extensions (`plugins`, `skills`, `subagent`, `difftest`), and unneeded tools (`fetch_url`, `powershell`). Furthermore, several wall-clock performance budget tests in `perf_budget_test.go` and `tools/concurrency_test.go` flake under CI load (issue 92).

This foundational spec performs "the cut": deleting dead packages, pruning unneeded tools, renaming the module path to `github.com/agent-fox-dev/agentkit-go`, enforcing the dependency allowlist test, and eliminating wall-clock threshold tests.

## Requirements

### Repository Deletions and Cleanup

1. The following directories and their contents shall be removed from the repository:
   - `provider/openai`, `provider/openairesponses`, `provider/google`, `provider/ollama`
   - `provider/credentials.go`, `provider/credentials_test.go`, `provider/salvage.go`, `provider/salvage_test.go`, `provider/repair.go`, `provider/repair_test.go`, `provider/asymmetry_test.go`, `provider/conformance_test.go`, `provider/fuzz_test.go`
   - `plugins`, `cmd/validate-plugins`
   - `skills`, `_skills/code-review`
   - `session`
   - `subagent`
   - `difftest`
   - `imagex`
   - `compaction`
   - `middleware`
   - `stop`
   - `examples/branching`, `examples/chat`, `examples/cleaner`, `examples/codesearch`, `examples/compaction`, `examples/deferred`, `examples/delegation`, `examples/flatline`, `examples/images`, `examples/interactive`, `examples/mcpserver`, `examples/middleware`, `examples/observability`, `examples/plugins`, `examples/session`, `examples/skills`, `examples/streaming`, `examples/testing`, `examples/tools`, `examples/triage`

2. The following individual files shall be removed:
   - `tools/fetch.go`, `tools/ssrf.go`, `tools/ssrf_test.go`, `tools/powershell.go`
   - `mcp/server.go`, `mcp/serve.go`, `mcp/server_test.go`
   - `wire/bind.go`
   - `deferred.go`, `deferred_test.go`
   - `audit.go`, `audit_test.go`
   - `images.go`, `images_test.go`
   - `skillsconfig.go`, `skillsconfig_test.go`
   - `resume.go`, `resume_test.go`, `resume_metadata_test.go`
   - `core/audit.go`, `core/audit_test.go`, `core/trace.go`, `core/plugin.go`
   - `perf_budget_test.go`
   - Root tests testing deleted subsystems (`plugin_wiring_test.go`, `middleware_wiring_test.go`, `events_test.go`, `compaction_usage_test.go`)

### Tools Image Read Without imagex

3. In `tools/tools.go`, `read_file` shall not import `imagex` or `golang.org/x/image`.
4. When `read_file` encounters a file whose leading bytes match image magic signatures (PNG, JPEG, GIF, WebP), it shall return an error result (`core.ErrResult("unsupported_file", ...)`) indicating that image reading is not supported and naming the detected format.
5. The `read_file` description and output schema shall remove references to image outputs.
6. The `tools.All` toolset and tool conformance tests shall not register `fetch_url`, `powershell`, or `subagent`.

### Root Agent Scaffolding Decoupling

7. In `agent.go`, references to `session.Recorder`, `session.NewRecorder`, `middleware.Chain`, and middleware methods (`Use`) shall be removed.
8. In `loop.go`, the call to `compaction.EstimateContextTokens` and the `Continue` method shall be removed.
9. In `providers.go`, registration of `openai`, `google`, and `ollama` shall be removed; only `anthropic` and `faux` remain available.
10. In `batch.go` and `nested.go`, calls to audit logging and tracer hooks shall be removed while preserving tool preparation, execution, event emission with `ParentToolUseID`, and concurrency semantics.

### Module Rename and Dependency Management

11. The root module path in `go.mod` shall be renamed to `github.com/agent-fox-dev/agentkit-go`.
12. All Go source and test files across the repository shall update their imports from `github.com/agentfox/agentkit-go/...` to `github.com/agent-fox-dev/agentkit-go/...`.
13. `codesearch/go.mod` shall declare `module github.com/agent-fox-dev/agentkit-go/codesearch`, require `github.com/agent-fox-dev/agentkit-go v0.0.0`, and set `replace github.com/agent-fox-dev/agentkit-go => ..`.
14. `go mod tidy` in the root module shall remove `golang.org/x/net`, `golang.org/x/image`, `golang.org/x/time`, and `code.dny.dev/ssrf`.
15. A new test in `internal/policy/deps_test.go` shall verify that `go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all` in the root module only outputs approved direct dependencies:
    - `github.com/bmatcuk/doublestar/v4`
    - `github.com/modelcontextprotocol/go-sdk`
    - `github.com/pelletier/go-toml/v2`
    - `github.com/tree-sitter/go-tree-sitter` and approved tree-sitter grammars
    - `go.starlark.net`
    Any unexpected direct dependency shall cause test failure.

### Elimination of Wall-Clock Duration Threshold Tests

16. All tests comparing elapsed duration (`time.Since` or `Elapsed`) against a fixed threshold to determine pass/fail shall be removed or converted to structural / count assertions. Specifically, the timing assertions in `tools/concurrency_test.go` checking operation durations shall be removed.
17. In `Makefile`, `difftest` and `examples/codesearch` references shall be removed from `test`, `vet`, `lint`, and `tidy`, and the `build-examples` target shall be deleted.
18. `make check` shall execute formatting, vet, linting, and testing across the root module and `codesearch` without errors.

## Design Decisions

1. **Delete obsolete packages rather than deprecating:** Deprecation incurs ongoing maintenance and testing costs for code with no users; per PRD 10, all unneeded code is removed immediately with no compatibility layer.
2. **Rename module path immediately:** The previous module path `github.com/agentfox/agentkit-go` diverged from the repository path `github.com/agent-fox-dev/agentkit-go`; renaming now ensures all subsequent PRs build against the canonical proxy path.
3. **Inspect image magic bytes directly in `read_file`:** Instead of pulling in `imagex` or image decoding libraries, `read_file` inspects the initial 16 bytes for standard headers (PNG, JPEG, GIF, WebP) and returns an actionable error, allowing complete deletion of `imagex` and `golang.org/x/image`.
4. **Retain `provider/faux` and existing `provider/anthropic` temporarily:** To ensure `make check` passes at this cut stage before Scope 2 rewrites Anthropic over the official SDK, `provider/faux` and the existing Anthropic provider are kept operational with their imports updated.
5. **Decouple Agent struct from session and middleware inline:** Rather than waiting for the driver rewrite, removing `rec *session.Recorder` and `chain middleware.Chain` from `agent.go` allows immediate deletion of the `session` and `middleware` packages while keeping the loop functioning.
6. **Enforce direct dependencies via automated allowlist test:** A test in `internal/policy/deps_test.go` reading `go list -m all` ensures no undeclared third-party dependencies enter the root module.

## Dependencies

| Spec | Status | Reason |
|---|---|---|
| `04_runner_and_tool_metadata` | active | Subprocess execution runner (`proc_windows.go`, `proc_unix.go`, `exec.go`) is retained while powershell is removed. |
| `06_tool_output_schemas` | active | Conformance tests and schema declarations for kept tools are preserved; output schemas for removed tools (`fetch_url`, `powershell`, `subagent`) are pruned. |
| `07_nested_tool_calls` | active | Retains reachability, tool policy resolution, and nested caller execution while removing deprecated audit and trace hook calls. |
| `08_code_mode` | active | Kept intact; continues to pass all conformance and smoke tests. |

## Verified External API

All retained direct external dependencies are pinned in `go.mod`:
- `github.com/bmatcuk/doublestar/v4`: Path glob matching.
- `github.com/modelcontextprotocol/go-sdk`: MCP client and pool integration.
- `github.com/pelletier/go-toml/v2`: TOML configuration parsing for MCP.
- `github.com/tree-sitter/go-tree-sitter` and grammars: Code outlining and structure.
- `go.starlark.net`: Code mode sandboxed Starlark runtime.
No new external APIs are introduced in this foundational scope.
