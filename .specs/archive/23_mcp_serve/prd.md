---
spec_id: "23"
spec_name: "mcp_serve"
title: "mcpserve: serve a Runner's tools over MCP to another harness"
status: "active"
created_at: "2026-10-16T06:17:08.118995Z"
updated_at: "2026-10-16T06:17:08.118995Z"
intent_hash: "0d6efcb108090b10df5286e6f5a30ee196857f93be5ef0bf095c32f1fee816cf"
schema_version: 2
source: "docs/prd/12-simplify-the-embedder-contract.md"
---
## Intent

Let a harness that is not AgentKit's own model loop (Claude Code, Pi, or any MCP client on the same machine) use an AgentKit toolset through one opt-in nested module, `mcpserve`. The module serves an `agentkit.Runner`'s tools over MCP: on the process's stdio, or over Streamable HTTP on a loopback port behind a bearer token. Every call runs through the runner's pipeline and guard. The module also produces the configuration text a harness needs to connect, so an embedder no longer writes its own MCP server. The root and sandbox modules still listen on no socket.

## Goals

1. `mcpserve.New(runner, mcpserve.Options{})` followed by `ServeLoopback(ctx)` answers `tools/list` with exactly one tool for each tool in `runner.Tools()`. Each listed tool has the same `name` and `description`, and an `inputSchema` that is JSON-equal to `json.Marshal(tool.InputSchema)`.
2. A `tools/call` runs `runner.Call` and returns `core.ToolResult.LLMText()` as its text, with `isError` equal to `!OK`. A call the guard blocked comes back as an `isError` result that carries `blocked_by_policy` and the guard's reason. It is never a protocol error.
3. Over HTTP, a request without the exact bearer token gets HTTP 401. AgentKit's own `mcp.Pool`, given the endpoint's URL and token, connects and runs a `code_mode` script end to end, with the script's nested `read_file` call going through the runner.
4. `ServeStdio` serves the same tools to a client that starts the program as a child process (AgentKit's `mcp.Pool` with a `Command`). It also serves them to a client connected through in-process pipes.
5. `Endpoint.ClaudeConfig(name)` and `Endpoint.PiConfig(name)` return the JSON that Claude Code's `--mcp-config` and Pi's `.pi/mcp.json` take. It has the shapes `examples/harness` uses today.
6. `mcpserve` is in `MODULES`, in the `make doc` loop and in `internal/policy`'s build gates. A policy test checks that it directly requires only the root module and the MCP Go SDK, and that its module graph adds nothing the root module's graph lacks. `make check` passes offline in every module. The no-listen check still covers the root and sandbox modules, and still passes.
7. `examples/harness` has no `server.go` and imports no MCP SDK package. It serves its tools through `mcpserve`, fills its sandbox with one `sandbox.Open` call, uses `guard.Allow`, `faux.ToolCall` and `faux.Text`, and its offline run still ends with "Harness example completed successfully".

## Non-goals

- MCP serving from the root or sandbox modules. The no-listen invariant for those two modules stays as it is.
- A general MCP server product. Out of scope: resources, prompts, sampling, elicitation, completions, `tools/list_changed` notifications, a tool set that changes after `New`, TLS, non-loopback addresses, a configurable port, several tokens, and token rotation.
- A `cmd/agentkit-mcp` command, and any change to `docs/cli.md`. The program that builds the toolset is the one that serves it.
- Serving `outputSchema`, `structuredContent`, `ToolResult.Data` as structured data, `ToolResult.Metadata`, or the `Terminate` vote over MCP. A harness has its own loop to end.
- Generating `Instructions` from the tools' `PromptGuidelines`. The embedder passes instructions, or puts `prompt.Build`'s output in the harness's system prompt, as `examples/harness` does.
- Configuration strings for a stdio server, or for harnesses other than Claude Code and Pi.
- Anything `Runner` does: preparation, the guard, nested calls, events. That is spec `21_toolset_runner`.
- `core.Named`, `guard.Allow`, `faux.ToolCall`, `faux.Text` and `sandbox.Open` themselves. Those are spec `22_embedder_helpers`, and this spec only uses them in `examples/harness`.
- The closing pass, which belongs to the later spec **`embedder_contract_docs`**. It covers settling the exported-API golden across every changed package (including whether `mcpserve` joins it), the end-to-end rewrite of the README consumer-contract table and of the architecture data-flow sections, and redoing PRD 12's §Intent measurement over the rewritten examples.

## Background

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`, decision D4 and §5) assigns MCP serving to a nested module so that "AgentKit serves no network API" stays true of the root and sandbox modules. PRD 10 had removed the old MCP server from the root module. Today the only MCP server in the repository is the one `examples/harness/server.go` writes by hand:

- **`mcpServer()`** creates `sdk.NewServer(&sdk.Implementation{Name: "agentkit", Version: "example"}, &sdk.ServerOptions{Instructions: …})`. It then adds one `sdk.Tool{Name, Description, InputSchema: json.Marshal(t.InputSchema)}` per served tool, using `{"type":"object"}` when the schema is nil. Its handler returns `&sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: res.LLMText()}}, IsError: !res.OK}`.
- **`serve(ctx)`** creates a 24-byte `crypto/rand` token, hex-encoded to 48 characters. It wraps `sdk.NewStreamableHTTPHandler(…, nil)` in a `subtle.ConstantTimeCompare` check of `Authorization: Bearer <token>` that answers 401 on a mismatch. It listens on `127.0.0.1:0` and serves with `ReadHeaderTimeout: 10s` and `BaseContext` set to ctx. Its `stop` function shuts down gracefully for at most 2 s and then forces a close, because a client's standalone SSE stream never ends on its own.
- **`mcpConfig(pi)`** returns `{"mcpServers":{"agentkit":{"type":"http","url":…,"headers":{"Authorization":"Bearer …"}}}}`. For Pi it adds `"exposure":"direct"`.
- **`main_test.go`'s `TestServeRequiresTheToken`** pins the URL shape, the token length, the 401/200 behaviour and both config shapes.

Spec 21 replaces the hand-rolled call pipeline in `server.go` with `Runner.Call`. The MCP code above stays in the example until this spec.

The repository already has the pieces a strict server needs:

- `mcp.NewPipeTransport(r, w, wire.Limits)` (`mcp/transport.go`) is a strict NDJSON `sdk.Transport`. Every inbound frame is bounded by `wire.FrameReader` and checked by `wire.Guard`, and EOF is held back until every pending call has been answered. Its doc calls it "the server's stdio mode", and `examples/codemode` and `examples/mcp` serve SDK servers over it.
- `wire.Guard(data, wire.Defaults())` is the bound the client applies to every inbound HTTP body and SSE event (REQ-SEC-11). PRD 09 records that the removed server answered over-size or malformed HTTP bodies with 413 or 400.
- `mcp.Pool` connects to a `URL` server with `Headers`, or spawns a `Command` server with exactly the `Env` it is given.

`sandbox/gatewayd` is the precedent for a nested module that listens:

- `go.mod` with `replace` lines;
- `New(Config) (*Server, error)`;
- `Logger *slog.Logger`, which defaults to `slog.New(slog.DiscardHandler)` and never logs a token, tool input or tool result;
- errors prefixed with the package name;
- policy tests TS-19-60 (module and direct set), TS-19-61 (graph subset), TS-19-62 (gates) and TS-19-63 (no-listen). The no-listen walker skips every directory that has its own `go.mod`, so a nested module is exempt by construction.

## Requirements

### R1. The module

A new nested module lives at `mcpserve/`:

- `go.mod` declares `module github.com/agent-fox-dev/agentkit-go/mcpserve` and `go 1.27`.
- It directly requires `github.com/agent-fox-dev/agentkit-go v0.0.0`, with `replace github.com/agent-fox-dev/agentkit-go => ../`, and `github.com/modelcontextprotocol/go-sdk` at the version the root `go.mod` requires (v1.8.0). Nothing else is a direct requirement.
- Its `go list -m all` names no module path that the root module's `go list -m all` lacks, apart from `mcpserve` itself.

It holds one package, `mcpserve`, with no `main` package and no `cmd/` directory. Non-test files import only:

- the standard library;
- the root packages `agentkit`, `core`, `mcp` and `wire`;
- `github.com/modelcontextprotocol/go-sdk/mcp`, plus that SDK's `jsonrpc` package if a connection wrapper needs it.

The package doc says three things: this is the one AgentKit module that serves MCP; it serves one runner's tools to a harness on the same machine; and it does so over stdio, or over loopback HTTP behind a bearer token. Errors are built with `fmt.Errorf` and prefixed `mcpserve: `.

### R2. `New` and the tool definitions

`New(r *agentkit.Runner, opts Options) (*Server, error)`:

- **Nil runner.** A nil `r` is refused with `mcpserve: Runner is nil`.
- **Snapshot.** New takes `r.Tools()` once. The served set never changes afterwards.
- **One MCP tool per tool.** `Name` and `Description` are taken verbatim. `InputSchema` is the bytes of `json.Marshal(t.InputSchema)`, or `{"type":"object"}` when `t.InputSchema` is nil. No output schema, annotations or `_meta` are declared.
- **Unservable schema.** If a tool's schema fails to marshal, or its top-level JSON is not an object whose `type` is `"object"`, New returns `nil` and an error naming the tool: `mcpserve: tool %q: input schema …`. It never registers a tool the SDK would reject.
- **Implementation info.** The server's info is `opts.Name` and `opts.Version`, and `opts.Instructions` goes to the SDK's `ServerOptions.Instructions`. An empty `Name` means `"agentkit"`. An empty `Version` means the version of `github.com/agent-fox-dev/agentkit-go/mcpserve` in the binary's build info (the replacement's version when replaced), or `(devel)` when there is none. That is the same algorithm as `sandbox/internal/buildver`.
- **Zero tools.** A runner with no tools is served, and lists no tools.

A `*Server` is immutable and safe for concurrent use. It may serve any number of loopback endpoints and one stdio session at the same time, all over the same tool set.

### R3. Calls

Each `tools/call` for a served tool:

- **Runs through the runner.** It runs `r.Call(ctx, name, arguments)`, with `arguments` passed as the request's raw bytes. Absent arguments are passed as empty, which the runner treats as `{}`. mcpserve adds no preparation, guard or validation of its own, and has no path around the runner. A direct call from the harness and a nested call from a `code_mode` script therefore both pass the runner's `Guard`.
- **Result text.** The result's content is one text item holding `res.LLMText()`, followed by one text item for each `core.TextBlock` in `res.Blocks`, in order. Other block kinds are dropped. `isError` is `!res.OK`.
- **Failures are results.** The handler never returns a Go error, so `invalid_arguments`, `blocked_by_policy`, `panic`, `handler_error` and `aborted` all reach the client as results with `isError` set and the runner's envelope as the text.
- **Cancellation.** The call's context is the request's. When the client cancels the request, the session ends or the endpoint closes, the runner sees a cancelled context and its result is `aborted`.
- **Concurrency.** Concurrent calls are allowed. The runner is safe for concurrent use.
- **Unserved names.** A `tools/call` naming a tool that is not served is answered by the SDK as a protocol error and never reaches the runner.

### R4. `ServeStdio`

`(*Server).ServeStdio(ctx context.Context) error` serves one MCP session on `os.Stdin` and `os.Stdout`. It uses the strict NDJSON transport `mcp.NewPipeTransport` under `wire.Defaults()`.

- **Return value.**
  - It returns `nil` when the client closes its end, after every reply already owed has been written.
  - It returns `ctx.Err()` when ctx ends. The session is closed first.
  - Any other end of the session returns a non-nil error prefixed `mcpserve: `. For example, an inbound frame that breaks `wire`'s limits ends the session and is never handed to the runner.
- **Done context.** A ctx that is already done returns `ctx.Err()` without reading.
- **Standard streams.** When `ServeStdio` returns, `os.Stdin` has been closed and `os.Stdout` has not. mcpserve writes nothing to stdout except protocol frames, and logs only through `Logger`.
- **Once per process.** It is documented as callable once per process. This is not enforced.

The same serving path is tested two ways. In-process, it runs over `io.Pipe` pairs with `mcp.Connect` and `mcp.NewPipeTransport` on the client side. As a child process, a test binary re-executed with a marker variable is connected through `mcp.Pool` with `ServerConfig.Command`. That test may skip on Windows.

### R5. `ServeLoopback`

`(*Server).ServeLoopback(ctx context.Context) (*Endpoint, error)`:

- **Done context.** A ctx that is already done returns `nil, ctx.Err()` and listens on nothing.
- **Listener.** It listens on TCP `127.0.0.1:0`. A listen failure returns `mcpserve: …`.
- **Token.** It generates a fresh token for each call: 24 bytes from `crypto/rand`, as 48 lowercase hex characters. A random-source failure is an error.
- **Endpoint.** It returns `&Endpoint{URL: "http://127.0.0.1:<port>/mcp", Token: token}`.
- **Authentication first.** Every request, on any path and with any method, must carry an `Authorization` header exactly equal to `Bearer <token>`. The comparison is constant-time. Otherwise the answer is 401, with `WWW-Authenticate: Bearer` and the body `unauthorized`, before any body is read. A refused request is logged at Warn with the peer address and never with the header value.
- **Path.** An authorized request for any path other than `/mcp` is 404.
- **Body guard.** An authorized request with a body has the body read up to `wire.Defaults().MaxMessageBytes`:
  - a body over that size is 413;
  - a body `wire.Guard` rejects (for example, a duplicate key or depth over the limit) is 400;
  - otherwise the body is replayed to the SDK's `NewStreamableHTTPHandler`, with default (stateful) options, and the SDK serves the request.
- **HTTP server.** The server uses `ReadHeaderTimeout` of 10 s, and its handler contexts derive from ctx.
- **Lifetime.** The endpoint serves until `Endpoint.Close` is called or ctx ends, whichever comes first. Either one:
  1. stops accepting connections;
  2. shuts down gracefully for at most 2 s;
  3. then force-closes what remains, which cancels in-flight calls (they return `aborted`).

  After that, connecting to the port is refused.
- **`Close`.** `(*Endpoint).Close() error` is idempotent, safe to call concurrently, and returns nil. On an `Endpoint` that `ServeLoopback` did not return (for example, the zero value) it does nothing and returns nil.
- **Independent endpoints.** Two `ServeLoopback` calls on one `Server` give independent endpoints, with distinct ports and tokens. Closing one does not affect the other.

### R6. Harness configuration strings

- **`ClaudeConfig(name)`.** `(*Endpoint).ClaudeConfig(name string) string` returns the JSON `{"mcpServers":{"<name>":{"type":"http","url":"<URL>","headers":{"Authorization":"Bearer <Token>"}}}}`. This is what Claude Code's `--mcp-config` takes.
- **`PiConfig(name)`.** `(*Endpoint).PiConfig(name string) string` returns the same object with `"exposure":"direct"` added to the server entry. This is what Pi's `.pi/mcp.json` takes.
- **Common rules.** For both methods:
  - an empty `name` means `"agentkit"`;
  - the output is always valid JSON, built with `encoding/json`, so any name is escaped correctly;
  - the output reflects the Endpoint's current `URL` and `Token` fields.

### R7. `Options` and logging

`Options` has four fields: `Name`, `Version`, `Instructions string` and `Logger *slog.Logger`. A nil `Logger` discards. The Logger receives:

- one Info record `tool call` per served call, with `tool`, `ok`, the error code when the call failed, and `duration`;
- one Warn record per refused HTTP request, with `peer` and a reason: a missing or wrong token, a body over the limit, or a rejected body;
- one Warn record when a stdio session ends with an error.

No record carries the token, the `Authorization` header, tool arguments, result text or `Detail`.

### R8. Build wiring and policy gates

- **Makefile.** `mcpserve` is added to `MODULES`, so `make test`, `build`, `vet`, `lint` and `tidy` run there. It is also added to the `make doc` module loop, which writes `docs/generated/mcpserve/mcpserve.txt`. The generated file is committed.
- **Build gates.** `internal/policy`'s `gateModules` gains `"mcpserve"`, so the cross-target (cgo off, four targets) and host-cgo build and vet gates cover it.
- **New policy tests.** The root module's `internal/policy` gains tests that:
  - check the module file: path, `go 1.27`, the root replaced at `../`, the direct set exactly the root module and the SDK, the SDK at the root's version, and the direct set passing `validateAllowlist` with the mcpserve path allowed;
  - check the graph-subset rule of R1;
  - check that `MODULES`, the doc loop and `gateModules` list `mcpserve`.
- **No-listen check.** The scope of the no-listen check (TS-19-63) is unchanged: non-test files of the root and sandbox modules. Its doc comment and failure message say that only nested modules (`sandbox/gatewayd`, `mcpserve`) may listen. A fixture case shows that a listener in a nested `mcpserve/` module with its own `go.mod` does not fail it, and the delivered tree passes it.
- **Unchanged.** The root module's `go.mod`, its direct set and graph snapshot (TS-13-65), `internal/policy/testdata` and the exported-API golden do not change (TS-19-64).
- **Offline.** `make check` passes with `GOPROXY=off` (TS-19-66), because mcpserve adds no module.

### R9. `examples/harness` on `mcpserve` and the helpers

- **No `server.go`.** `examples/harness/server.go` is deleted. The code it still held after spec 21 that is not the MCP server keeps its behaviour in `examples/harness/calls.go`: the per-call log and the call and blocked counts built from the toolset's hooks, plus `summarize`, `firstLine`, `truncate` and `errShellBlocked`.
- **Imports and listening.** No non-test file of the example imports a `github.com/modelcontextprotocol/go-sdk` package or `net`, and none calls a listen API.
- **Serving.** `main.go` serves the runner with `mcpserve.New(runner, mcpserve.Options{Instructions: …})` and `ServeLoopback(ctx)`, with `defer ep.Close()`. It prints `MCP server at <ep.URL>`.
- **Harness runners.** `runHarness` and the three harness runners take `*mcpserve.Endpoint`:
  - `claude` passes `ep.ClaudeConfig("agentkit")` to `--mcp-config`;
  - `pi` writes `ep.PiConfig("agentkit")` to `.pi/mcp.json`;
  - `scripted` connects `mcp.Pool` to `ep.URL` with the header `Authorization: Bearer <ep.Token>`.
- **Sandbox.** The program chooses the source first: `sandbox.GitRepo{URL, Ref, Depth: 1}` for `--repo`, `sandbox.LocalDir{Path: dir, ExcludeGit: true}` for `--dir`, or `sandbox.LocalDir{Path: sample}`. It then creates, fills and gets the proxies with one `sandbox.Open(ctx, p, spec, src, sandbox.ToolOptions{})` call, with `defer sb.Close(ctx)`. It calls none of `Provider.Create`, `Sandbox.Sync` or `Sandbox.Tools`. The account prints `filled the workspace from <the sample project | dir | repo>` in place of the three per-source sync lines.
- **Guard and faux.** `guard.Allow(allowed...)` replaces `guard.Restricted(guard.Options{AllowedPrograms: allowed})`. In the scripted harness, `faux.ToolCall(…)` and `faux.Text(…)` replace the two `faux.Turn{…}` literals.
- **`go.mod`.** It requires `github.com/agent-fox-dev/agentkit-go/mcpserve v0.0.0`, with `replace … => ../../mcpserve`. After `go mod tidy`, the SDK is no longer a direct requirement.
- **Tests.**
  - `TestServeRequiresTheToken` is removed, because mcpserve's own tests cover it.
  - `TestHarnessArgs` uses `&mcpserve.Endpoint{}`.
  - `TestExampleRunsOffline` expects `filled the workspace from the sample project` in place of `synced the sample project: 7 files`, and otherwise passes as before ("imported 12 tools over MCP: agentkit__code_mode, …", "verdict: pass", "Harness example completed successfully").
  - A structural test pins the bullets above: no `server.go`, no SDK import, exactly one `sandbox.Open`, and no `faux.Turn{` literal.

### R10. Documentation

These updates are made in the same change, without the end-to-end rewrites that belong to `embedder_contract_docs`:

- **`make doc`.** It is run, and `docs/generated/` is committed.
- **`docs/api.md`.**
  - The intro sentence says the library's root and sandbox modules listen on no socket, and that two nested modules serve: `sandbox/gatewayd` and `mcpserve`.
  - There is a nested-module row for `mcpserve`.
- **`docs/architecture.md`.** Update:
  - Shape: six nested modules, with an `mcpserve/` entry; the `examples/harness` entry requires `mcpserve` too; the gates and allowlist sentence.
  - The "serves no network API" paragraph.
  - A Package graph row for `mcpserve`: `.`, `core`, `mcp`, `wire`, and the go-sdk.
  - The Testing layout module list.
- **`docs/configuration.md`.** It gains a section `## MCP serving (mcpserve.Options)`. The section covers `New`'s refusals, the four options, the `ServeLoopback` token, path, body limits and lifetime, `ServeStdio`'s streams, and the two config strings. The "Limits on untrusted input" section gets one sentence on mcpserve's inbound bounds.
- **`docs/DEPS.md`.** It gets a paragraph for the `mcpserve` module: no new module, the direct set, and the graph tests.
- **`docs/releasing.md`.** `mcpserve/vX.Y.Z` is pushed alongside `vX.Y.Z` and `sandbox/vX.Y.Z`, so that Go resolves the module.
- **`README.md`.** Update the opening "serves no network API" sentence, add a short `mcpserve` paragraph and snippet beside the gateway's, update the "driver is optional" paragraph, the packages table row, and the make-targets module lists.
- **`AGENTS.md`.** Add the line `mcpserve/  # Nested module: MCP serving of a Runner's tools` to the repository layout.
- **Example docs.**
  - `examples/harness/README.md`: replace "The code an embedder copies" with the `sandbox.Open` → `codemode.New` → `agentkit.NewRunner` → `mcpserve` sequence, and update step 5 and the sample output.
  - `examples/README.md`: update the harness row.
- **Erratum.** `docs/errata/19_sandbox_gateway.md` gains an entry naming PRD 12 and this spec. It records that 19-REQ-9.4's "exempts only the gatewayd module" now reads "exempts nested modules, gatewayd and mcpserve", and that the gate list gained `mcpserve`.

## Design Decisions

1. **`New` returns `(*Server, error)`, not `*Server` as PRD 12 §5 sketches.** It has to convert every schema before the SDK sees it. The SDK's `AddTool` rejects a tool whose input schema is missing or not of type object (by panicking, as recalled; unverified). A constructor that can fail returns an error in this codebase (`gatewayd.New`).
2. **A nil `InputSchema` is served as `{"type":"object"}`, and a non-object top-level schema is refused.** That substitution is what the harness does today. Silently rewriting a schema that has its own top-level type would change what the harness's model reads.
3. **The stdio transport is the repository's `mcp.NewPipeTransport` under `wire.Defaults()`, not the SDK's own stdio transport.** The input says the SDK's, but REQ-SEC-11 holds every inbound frame to `wire`'s rules before the SDK decodes it. This transport already does that, waits for pending replies at EOF, and is documented as "the server's stdio mode".
4. **Inbound HTTP bodies are bounded and checked with `wire.Guard` (413 or 400) after the token check.** This is the same trust-boundary rule the client applies, and the removed server answered the same way (PRD 09). Checking the token first means an unauthenticated peer cannot make the server read a body.
5. **The token is checked before the path, and only `/mcp` is served.** An unauthenticated request learns nothing about the server. The URL the configs carry is the only route.
6. **The token is 24 random bytes as 48 hex characters, and the listener is `127.0.0.1:0`; neither is configurable.** That is what the harness ships and tests. PRD 12 limits the module to a harness on the same machine.
7. **The endpoint's lifetime is `Close` or ctx, whichever comes first, with a 2 s graceful shutdown and then a forced close.** A client's standalone SSE stream never ends on its own (the harness's comment). Tying the endpoint to ctx means a caller that forgets `Close` still stops serving.
8. **HTTP sessions are stateful (the SDK's default options).** That is what the harness serves Claude Code with today. AgentKit's client works against it with `DisableStandaloneSSE`.
9. **`ServeStdio` closes stdin and leaves stdout open.** Closing stdin is what unblocks the read loop on cancellation. Leaving stdout open lets the program exit cleanly, which matches the SDK's own stdio behaviour as recalled (unverified).
10. **A result's text is `LLMText()` followed by the text blocks of `Blocks`. `Data`, `Metadata`, `Terminate` and output schemas are not served.** That is exactly what AgentKit's driver shows a model (`toolResultMessage` in `batch.go`). Structured output over MCP would mean promising that every tool's `Data` conforms to its `OutputSchema`, which is a separate decision.
11. **mcpserve serves `Runner.Tools()`, snapshotted at `New`.** PRD 12 §2 names `Tools()` as "what … an MCP server … should see". A fixed set needs no `list_changed` handling.
12. **No `cmd/agentkit-mcp`.** PRD 12 left this to spec time and leaned against it: a toolset belongs to the program that builds it, so that program serves it.
13. **Logging follows gatewayd's rule: structured records, never a secret, input or result.** A harness's tool arguments and outputs are workspace content, and the token is a credential.
14. **`Version` defaults to the module's build-info version, or `(devel)`, using the `buildver` algorithm without importing the sandbox module.** PRD 12 says "the module version", and mcpserve must not require the sandbox module.
15. **Inbound limits are `wire.Defaults()` with no option.** 16 MiB per message is far above any tool call. The client exposes `Limits` because its servers are arbitrary, while this server's client is a harness the embedder starts.
16. **The harness rewrite onto `sandbox.Open`, `guard.Allow`, `faux.ToolCall` and `faux.Text` happens here.** The split assigns `examples/harness` wholly to this spec. `22_embedder_helpers` leaves it untouched.
17. **The harness fills its sandbox with one `sandbox.Open` and prints `filled the workspace from …` instead of the file and byte counts.** `sandbox.Open` returns no `SyncResult`. Keeping a separate `Sync` only to print counts would preserve the `Create`/`Sync`/`Tools` chore that PRD 12 D7 says must shrink to one line.
18. **The harness's leftover hooks move to `calls.go`.** The acceptance criterion is that `server.go` is gone. Spec 21 owns what those hooks do, so they move unchanged.
19. **The no-listen check is not extended to mcpserve, and its message is corrected.** PRD 12 §8.11 says so. The walker already exempts every nested module, so only the wording and a fixture change, and 19-REQ-9.4's text is recorded in spec 19's erratum.
20. **The exported-API golden is not touched.** This spec changes no root-module package. Whether nested modules join the golden is `embedder_contract_docs`'s call.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `21_toolset_runner` | depends on | `mcpserve.New` takes `*agentkit.Runner` and serves `Runner.Tools()` through `Runner.Call`. The harness's runner, and the hooks this spec moves to `calls.go`, are spec 21's. |
| `22_embedder_helpers` | depends on | The harness rewrite uses `sandbox.Open`, `guard.Allow`, `faux.ToolCall` and `faux.Text`. |
| `20_workspace_package` | depends on | mcpserve's tests build `code_mode` and the built-in tools with `workspace.Open` and `tools.For`. |
| `19_sandbox_gateway` | modifies | TS-19-63's no-listen message and fixtures and TS-19-62's `gateModules` list change, and 19-REQ-9.4's wording gets an erratum entry. TS-19-64 and TS-19-66 must still pass. |

## Verified External API

`github.com/modelcontextprotocol/go-sdk v1.8.0`, imported as `sdk "github.com/modelcontextprotocol/go-sdk/mcp"`. The module cache is outside the repository, so the signatures below come from call sites in the repository that compile today.

| Symbol | Signature as used | Source / status |
|---|---|---|
| `sdk.NewServer` | `NewServer(impl *sdk.Implementation, opts *sdk.ServerOptions) *sdk.Server` | `examples/harness/server.go`, `mcp/mcp_test.go`, `examples/codemode/main.go` (opts may be nil) |
| `sdk.Implementation` | `{Name, Version string}` | same |
| `sdk.ServerOptions` | has `Instructions string` and `Capabilities *sdk.ServerCapabilities` | `examples/harness/server.go`, `mcp/mcp_test.go` |
| `(*sdk.Server).AddTool` | `AddTool(t *sdk.Tool, h func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error))` | `examples/harness/server.go`. **Unverified**: that it panics on a missing or non-object `InputSchema`, and that it does not validate arguments against the schema. |
| `sdk.Tool` | `{Name, Description string; InputSchema any}` (given a `json.RawMessage`) | `examples/harness/server.go`, `examples/codemode/main.go` |
| `sdk.CallToolRequest` | `req.Params.Arguments` is assignable to `json.RawMessage` | `examples/harness/server.go`, `examples/codemode/main.go` |
| `sdk.CallToolResult` | `{Content []sdk.Content; IsError bool}` | same |
| `sdk.TextContent` | `&sdk.TextContent{Text: string}` (implements `sdk.Content`) | same |
| `sdk.NewStreamableHTTPHandler` | `NewStreamableHTTPHandler(getServer func(*http.Request) *sdk.Server, opts *sdk.StreamableHTTPOptions)`; the result has `ServeHTTP` | `examples/harness/server.go` (nil opts), `mcp/mcp_test.go` (`&sdk.StreamableHTTPOptions{Stateless: true}`) |
| `(*sdk.Server).Run` | `Run(ctx context.Context, t sdk.Transport) error` | `mcp/transport_test.go`. **Unverified**: what it returns on a clean EOF and on a Read error. mcpserve normalizes the result per R4. |
| `(*sdk.Server).Connect` | `Connect(ctx, t sdk.Transport, opts) (*sdk.ServerSession, error)`; `(*ServerSession).Close() error` | `examples/codemode/main.go`, `examples/mcp/main.go` |
| `sdk.Transport` | interface `Connect(context.Context) (sdk.Connection, error)` | `mcp/transport.go` (`connTransport`) |
| `sdk.Connection` | `Read(ctx) (jsonrpc.Message, error)`, `Write(ctx, jsonrpc.Message) error`, `Close() error`, `SessionID() string` | `mcp/transport.go` (`strictConn`), with `jsonrpc` from `github.com/modelcontextprotocol/go-sdk/jsonrpc` |

Symbols from earlier specs of this split that are not yet in the tree (**NOT FOUND**, so their PRDs' signatures are assumed):

- `agentkit.NewRunner(ts Toolset) (*Runner, error)`, `(*Runner).Tools() []core.Tool` and `(*Runner).Call(ctx, name string, input json.RawMessage) core.ToolResult` (spec 21);
- `sandbox.Open(ctx, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error)`, `guard.Allow(programs ...string) core.BeforeToolCall`, `faux.ToolCall(id, name, args string) Turn` and `faux.Text(s string) Turn` (spec 22);
- `workspace.Open(root string, opts workspace.Options) (*workspace.Workspace, error)` and `tools.For(ws, tools.Options) ([]core.Tool, error)` (spec 20).
