# Dependency rulings

Why a third-party module is in AgentKit, and on what terms. The rule since
[PRD 09](prd/09-replace-hand-rolled-code-with-libraries.md) is that a
maintained library replaces hand-rolled infrastructure when it carries the
same guarantees. Each module here is one where that was decided on purpose.

## The allowlist

`internal/policy/deps_test.go` lists the root module's approved direct
dependencies, and `TestDirectDependenciesAreApproved_TS09_16` fails, naming the
module, when `go list -m` reports a direct dependency that is not on it:

| Module | For |
|---|---|
| `github.com/bmatcuk/doublestar/v4` | glob matching in `find_files` and the ignore engine |
| `github.com/modelcontextprotocol/go-sdk` | the MCP client and pool |
| `github.com/pelletier/go-toml/v2` | TOML parsing for MCP configuration (`internal/toml`) |
| `github.com/tree-sitter/…`, `github.com/tree-sitter-grammars/…` | tree-sitter outlines and their grammars |
| `go.starlark.net` | code mode's Starlark runtime |
| `github.com/anthropics/anthropic-sdk-go` | the Anthropic wire: transport, auth, retries, stream framing, Vertex |
| `golang.org/x/oauth2` | Google credentials for the Vertex AI deployment |
| `github.com/aws/aws-sdk-go-v2`, `github.com/aws/aws-sdk-go-v2/…` | AWS configuration and credentials for the Bedrock deployment |

Adding a direct dependency means adding it there, with its reason, and a ruling
here. The `codesearch` module is separate and not covered: its zoekt graph stays
out of the root module's.

## `github.com/anthropics/anthropic-sdk-go`

**Used by.** `provider/anthropic`, for every request.

**Version.** `v1.79.1`, pinned and moved deliberately.

**Why this module.** It owns what a hand-rolled wire had to: the HTTP
transport, retries with `Retry-After`, SSE framing, and the Vertex AI request
rewriting and OAuth. AgentKit still writes the request body itself and sends
it through the client (`option.WithRequestBody`), because the SDK's request
types re-encode a replayed `tool_use` input and its bytes must reach the wire
as the model wrote them.

**What is pulled in.** The Vertex option brings `golang.org/x/oauth2` and
Google's API transport; the module also requires the AWS SDK for its Bedrock
option.

## `go.starlark.net`

**Used by.** `codemode`, the tool that runs model-written scripts. Nothing
else imports it.

**Version.** `v0.0.0-20261005163335-bcb1a1a55bf9`. The module publishes
pseudo-versions only; there are no tagged releases to pin to.

**Why this module.** A code-mode script is written by a model and runs inside
the host process, so the interpreter has to be safe to run without trusting
the script:

- **No host I/O of its own.** Starlark has no file, network, environment,
  process or clock primitives. A script can reach only the builtins the host
  passes in, and `codemode` passes in the bound tools and three helpers.
  Module loading goes through a host hook, which `codemode` sets to refuse
  every module.
- **A step budget.** `Thread.SetMaxExecutionSteps` counts execution steps and
  cancels the thread when the budget is spent. A runaway loop therefore ends
  as `step_limit_exceeded` rather than by burning a CPU until the wall-time
  limit fires.
- **Cancellable.** `Thread.Cancel` stops a running script from another
  goroutine, which is how the wall-time limit and a cancelled run stop one.
- **Deterministic and pure Go.** There is no cgo and no background
  goroutines, and it builds for every target `internal/policy` checks.
- **A language models write.** It is a Python dialect. The tool's description
  spells out what differs (no exceptions, no imports, no classes).

**What is pulled in.** `codemode` imports `go.starlark.net/starlark`, and that
brings in `syntax`, `resolve` and two internal packages. The module's REPL
dependencies (`chzyer/readline`, `x/term`) and its protobuf support are not
imported. `codemode`'s `TestDepsDefaultsAndIsolation_TS08_44` checks this.

**Alternatives considered.** An embedded JavaScript engine is larger, has
ambient capabilities to remove rather than none to add, and has no step
counter. A WebAssembly sandbox would need a compiler for the model's language
as well.
