# Simplify the embedder contract

Status: **draft, decisions settled** (2026-10-15). Nothing here is
implemented. The decisions in §0 were workshopped over the three sandbox
examples and `examples/harness`, and are recorded with their outcomes. Next
step: this document becomes the `prd.md` of spec packages 20 to 24 (§7), and
the spec work begins. Details left to settle at spec time are marked
*spec time*.

Builds on PRD 08 (code mode), PRD 10 (the hands and one driver) and PRD 11
(sandboxes). It changes the shape of the contract those PRDs left, not what
the pieces do.

## Intent

Setting up any non-trivial scenario on AgentKit today takes more lines of
wiring than of scenario. Measured over the four examples that use a
sandbox, code mode or both, the same five chores recur, and the point of
each example is a small part of its file:

| Chore | `codemode` | `sandbox` | `gateway` | `harness` |
|---|---|---|---|---|
| Pick tools by name out of `tools.All` with a `switch` | 8 lines | 5 | 5 | 0 |
| Build a `faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall(…)}, StopReason: …}` | 9 | 5 | 5 | 6 |
| `Create`, `Sync`, `Tools`, `Export`, `Close`, each with its error check | 0 | 30 | 30 | 35 |
| A guard and `agentkit.New(agentkit.Config{…})` | 10 | 12 | 12 | 0 |
| Run a tool call outside the agent loop (prepare, guard, handler, nested caller) | 0 | 0 | 0 | 95 |

The last row is a capability gap, not friction: the pipeline that runs a
tool call — argument preparation, the guard, the handler, the after
interceptor, the nested caller a code-mode script needs, the execution
events — exists only inside the agent loop, so anything that is not
AgentKit's own model loop (another harness over MCP, a test, an embedder's
own scheduler) rebuilds it. `examples/harness/server.go` is that rebuild,
and it is the part of the example a reader should not have had to read.

Two shapes in the contract cause the rest. `agentkit.Config` holds the
model side (`Client`, `Provider`, `Model`, `Effort`, `System`, `Prefix`,
`MaxTurns`, `MaxCostUSD`, `Timeout`, `Prune`, `MaxTokens`) and the tool side
(`Tools`, `Policy`, `Guard`, `After`) in one struct, so the tool side cannot
be built, validated or used on its own. And the workspace — the containment
boundary every file tool, the ignore engine, the walker and the sandbox
agent are built on — is a type inside `tools`, configured through
`tools.Options`, so the thing every scenario starts from reads as a detail
of one package.

This PRD splits the tool side out of the driver as a **toolset** with a
**runner**, makes the **workspace** a package of its own that tools are
built *for*, adds an **MCP serving** module so another harness can use a
toolset, and adds the small helpers the examples keep rewriting. It is a
breaking change to `agentkit.Config`, `tools.Options` and the workspace
API, taken deliberately (§0 D6).

## 0. Decisions

### D1. The tool side leaves `Config`: a `Toolset`, built into a `Runner`

**Decided: breaking.** `Config` loses `Tools`, `Policy`, `Guard` and
`After`. They move to `agentkit.Toolset`, a value `Config` holds in one
field. `agentkit.NewRunner(ts Toolset) (*Runner, error)` validates a
toolset exactly as `New` validates those fields today — the cycle check,
the terminating-through-wrapper check, a reachable shell with no `Guard`
(`core.ErrUnguardedExecute`) — and `New` calls it. An additive variant
(keeping the four fields as sugar) was considered and rejected: two ways
to say the same thing is the opposite of what this PRD is for, and nothing
on the current module path depends on the old shape (D6).

### D2. A `Runner` runs the pipeline without a model

**Decided.** `Runner.Call` runs one call and `Runner.Batch` runs a batch
through the same steps a model's call goes through today: `core.NewToolUse`,
`core.PrepareArguments`, `Guard`, the handler with a nested caller attached
when the tool declares `ReachableTools`, `After`, and the
`ToolExecutionStart`/`End` events. Events go to an optional
`Toolset.Events func(core.Event)` sink; there is no stream object on the
runner. The agent loop becomes a consumer of the runner (it hands the
run's own event stream in as the sink, per run), so the driver's
`nested.go` and most of `batch.go` are the runner's implementation, not a
second copy. A wrapper tool such as `code_mode` therefore works from a
`Runner.Call` with no agent, which closes the gap `docs/architecture.md`
documents today ("calling code mode outside an agent run fails … with
`ErrNoNestedCaller`").

### D3. The workspace is a package: `workspace`

**Decided.** `tools.Workspace`, `tools.NewWorkspace`, `tools.Walk`,
`tools.WalkOptions`, `tools.IgnoreOptions`, `tools.NoGlobalExcludes` and
`tools.Normalize` move to a new root-module package `workspace`. The
workspace owns *where* and *what is visible*: the root, containment
(`Resolve`, `Rel`, `CheckWriteTarget`), the ignore rules and the walk. The
ignore configuration moves with it: it is a property of the workspace, not
of the tools over it, and today it has to be passed twice (once in
`tools.Options.Ignore`, once in `sandbox.LocalDir.Ignore`) because the
workspace does not hold it. `tools` keeps the engines over a workspace
(the file tools, the shell tools, the symbol table, the reference search,
the index seam) and builds them *for* one: `tools.For(ws, opts)` replaces
`tools.All(tools.Options{Workspace: ws})`, and the workspace is required,
not optional. `(*Workspace).References` becomes `tools.References(ctx, ws,
target, opts)`: it is a tool engine, and it would otherwise drag `outline`
and the reference cache into `workspace`.

### D4. MCP serving is a nested module, `mcpserve`

**Decided: nested.** "AgentKit serves no network API" stays true of the
root and sandbox modules, and `internal/policy` keeps checking that no
non-test file there calls `net.Listen`. `mcpserve` is a nested module like
`sandbox/gatewayd`: it requires the root module and the MCP Go SDK (already
a root dependency, for the client), and serves a `Runner`'s tools over
stdio or over Streamable HTTP on a loopback port behind a bearer token. It
is the only piece of this PRD that listens on anything.

### D5. The helpers the examples keep rewriting become API

**Decided.** `core.Named` (pick tools by name, erroring on a name that is
not there), `guard.Allow` (an allowlist guard in one call), `faux.ToolCall`
and `faux.Text` (a turn in one call), and `sandbox.Open` (create, fill and
get the proxies in one call). Each is sugar over an existing API and
removes a recurring block from the examples; none adds a concept.

### D6. Breaking is acceptable now

**Decided.** The module is pre-1.0, and this PRD is about agentkit-go
alone: the consumers it has to carry are the ones in this repository — the
nested modules `codesearch`, `sandbox` and `sandbox/gatewayd`, and the
examples — and every one of them is rewritten in the spec that changes
what it uses. The migration table in §6 is for them, and for whoever comes
later. Spec 13's test TS-13-67 pins the exported API of `agentkit`, `core`,
`tools` and `codemode` to a golden file; this PRD changes three of the four
on purpose, so the golden is regenerated and
`docs/errata/13_sandbox_core.md` records why the invariant was retired as a
*frozen* one and what replaces it (the golden stays, as a change detector).

### D7. The examples are the acceptance test

**Decided.** Each example is rewritten on the new contract in the spec that
changes what it uses, and the rewrite is measured: the chores in the table
above go to zero or one line each, `examples/harness` loses its pipeline
(`server.go`'s `call`, `nested`, the recovery and the MCP wiring) to
`Runner` and `mcpserve`, and the README quickstart reads as one idea per
line. A spec whose example did not get shorter did not do its job.

## Goals

1. **A toolset is a value an embedder can build, validate and use with no
   model**: `agentkit.NewRunner(ts)` returns a `Runner` whose `Call` and
   `Batch` run the full pipeline, with a code-mode tool working from it.
2. **The workspace is the first thing an embedder opens**, in its own
   package, holding its own ignore rules, and the tools are built for it.
3. **Another harness can use a toolset over MCP** with one nested module:
   `mcpserve.New(runner).ServeLoopback(ctx)` and a config string for
   Claude Code or Pi.
4. **The recurring blocks in the examples become one call each.**
5. **The driver gets smaller**, not larger: it is the loop over a `Runner`,
   and `nested.go` and the batch executor are the runner's.

## Non-goals

- A plugin or registry for tools, a configuration file, or any global state.
- A virtual filesystem under the workspace (PRD 11 D1 still holds).
- Changing what any tool does, what the guard checks, how the sandbox
  isolates, or the sandbox protocol.
- Serving MCP from the root or sandbox modules.
- An MCP server that is a general product: `mcpserve` serves one runner's
  tools to a harness on the same machine, and that is all.
- Keeping the old API alongside the new one.

## Background

What a scenario costs today, concretely. The README quickstart:

```go
ws, err := tools.NewWorkspace(".")
ts, err := tools.All(tools.Options{Workspace: ws})
client, _, err := anthropic.Resolve(anthropic.OSEnv{})
agent, err := agentkit.New(agentkit.Config{
	Client: client, Model: "claude-opus-5-5", Tools: ts,
	Guard: guard.Restricted(guard.Options{AllowedPrograms: []string{"go", "git"}}),
})
```

Binding four tools into code mode (README and `examples/codemode`):

```go
var bound []core.Tool
for _, t := range ts {
	switch t.Name {
	case "list_files", "find_files", "read_file", "search_files":
		bound = append(bound, t)
	}
}
cm, info, err := codemode.New(bound, codemode.Options{})
```

A sandbox (`examples/sandbox`, `examples/gateway`, `examples/harness`):

```go
sb, err := p.Create(ctx, spec)
defer sb.Close(ctx)
if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: dir, Ignore: tools.NoGlobalExcludes()}); err != nil { … }
proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
```

And the harness example's reason to exist: 95 lines in `server.go` that
re-implement `nested.go` so that `code_mode` can run from an MCP handler,
plus 60 lines of MCP server.

## 1. The `workspace` package

```go
package workspace

// Options configures a workspace. The zero value reads the real ignore
// environment, global excludes included.
type Options struct {
	Ignore IgnoreOptions
}

// Open resolves root to its absolute canonical form and reads nothing else.
func Open(root string, opts Options) (*Workspace, error)

type Workspace struct { /* unexported */ }

func (w *Workspace) Root() string
func (w *Workspace) Resolve(p string) (string, error)   // containment: symlinks resolved, outside refused
func (w *Workspace) Rel(abs string) string
func (w *Workspace) CheckWriteTarget(abs string) error
func (w *Workspace) Ignore() IgnoreOptions
// Walk visits the entries under sub (""/"." for the root), skipping what the
// ignore rules exclude, as tools.Walk does today.
func (w *Workspace) Walk(ctx context.Context, sub string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error
// Ignored reports whether a workspace-relative path is excluded by the rules.
func (w *Workspace) Ignored(rel string, isDir bool) bool

type IgnoreOptions struct { /* as tools.IgnoreOptions today */ }
func NoGlobalExcludes() IgnoreOptions
type WalkOptions struct { /* as tools.WalkOptions today */ }
func Normalize(p string) (string, error)
```

`Root` becomes a method: the struct now carries the ignore state, and an
exported field on a type with invariants is how a workspace ends up with
a root nobody resolved. `Ignored` is the one new capability; it exposes the
engine that already exists so an embedder can ask the question the tools
ask, and it is cheap. The ignore engine, the walker and the path code move
file by file (`tools/path.go`, `tools/walk.go`, `tools/ignore.go`); their
tests move with them.

`workspace` imports only the standard library and `bmatcuk/doublestar`.
`tools` imports `workspace`; `workspace` never imports `tools`.

## 2. `Toolset` and `Runner`

```go
package agentkit

// Toolset is the tool side of an agent: what can be called, under what
// policy, past which guard, with what afterwards.
type Toolset struct {
	Tools  []core.Tool
	Policy core.ToolPolicy
	Guard  core.BeforeToolCall
	After  core.AfterToolCall
	// Events receives ToolExecutionStart and ToolExecutionEnd for every call
	// a Runner runs outside an agent run. Nil discards. An agent run replaces
	// it with the run's own stream for the calls of that run.
	Events func(core.Event)
}

// NewRunner validates ts — the policy resolved, no cycle, no terminating
// tool behind a wrapper, no reachable shell without a Guard — and returns
// the pipeline over it.
func NewRunner(ts Toolset) (*Runner, error)

type Runner struct { /* unexported */ }

// Tools is the resolved set, in policy order: what a model, an MCP server
// or a prompt should see.
func (r *Runner) Tools() []core.Tool
// Reachable is the transitive closure, as Agent.ReachableTools is today.
func (r *Runner) Reachable() []core.Tool
// Call runs one call: unknown tool, invalid arguments, a block, a panic and
// an abort are results, never errors. A wrapper's nested calls go through
// this runner.
func (r *Runner) Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult
// Batch runs calls with the driver's batch rules: preparation in order, the
// abort decision once, handlers concurrent unless a Sequential tool is
// among them, one result per call in call order. A result's Terminate
// carries an interceptor's vote.
func (r *Runner) Batch(ctx context.Context, calls ...core.ToolUseBlock) []core.ToolResult

// Config is the model side, plus the toolset.
type Config struct {
	Client     *sdk.Client
	Provider   core.ProviderClient
	Model      string
	Effort     Effort
	System     string
	Prefix     []core.Message
	Toolset    Toolset
	MaxTurns   int
	MaxCostUSD float64
	Timeout    time.Duration
	Prune      PruneOptions
	MaxTokens  int
}

// Runner is the agent's runner, for an embedder that also serves or tests
// the same tools. Safe during a run.
func (a *Agent) Runner() *Runner
```

`New` calls `NewRunner(cfg.Toolset)` and keeps every refusal it has today,
with the same errors. `Agent.ReachableTools` stays and is
`a.Runner().Reachable()`. The loop, `executeBatch`, `nestedCaller` and
`invokeHandler` move under the runner; the agent's own event stream is
what the runner emits to during a run. *Spec time*: whether `Batch` takes
the `Assistant`, `Batch` and `Index` fields of `BeforeToolCallContext` from
an optional argument or leaves them zero for a call no model made.

What the runner does not do: own a transcript, a cost, a turn count or a
stop reason. Those are the loop's.

## 3. `tools` after the move

```go
package tools

type Options struct {
	SpillDir     string
	DisableSpill bool
	Env          []string
	Symbols      SymbolOptions
	Index        Index
}

// For builds the built-in tool set over ws: read, write, edit, list, find,
// search, the three navigation tools and the two shell tools, plus
// Index.Tools() when Index is set.
func For(ws *workspace.Workspace, opts Options) ([]core.Tool, error)

// References finds the usages of a declaration in ws, as find_references
// does, with no model and no cache.
func References(ctx context.Context, ws *workspace.Workspace, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)
```

`Options.Workspace` and `Options.Ignore` are gone: the workspace is the
argument, and it holds its ignore rules. `All` is renamed to `For` because
the old name said nothing about the workspace it needs, and because a
rename makes every call site a compile error rather than a silently
different default. The individual constructors stay unexported.

## 4. The helpers

```go
// core
func Named(ts []core.Tool, names ...string) ([]core.Tool, error) // in names order; an unknown name is an error naming it and the names present

// guard
func Allow(programs ...string) core.BeforeToolCall // Restricted(Options{AllowedPrograms: programs})

// provider/faux
func ToolCall(id, name, args string) Turn // one tool_use block, StopReasonToolUse
func Text(s string) Turn                  // one text block, StopReasonStop

// sandbox
// Open creates a sandbox from spec, fills its workspace from src (a nil
// src fills nothing) and returns it with its proxy tools. On any failure
// after Create the sandbox is closed before Open returns.
func Open(ctx context.Context, p Provider, spec Spec, src Source, opts ToolOptions) (Sandbox, []core.Tool, error)
```

`faux.FauxToolCall`, `faux.FauxText` and `faux.FauxThinking` stay: they are
blocks, for a turn that holds several. `Turn` stays public.

## 5. `mcpserve`

```go
module github.com/agent-fox-dev/agentkit-go/mcpserve

type Options struct {
	Name, Version string // the server's implementation info; defaults "agentkit" and the module version
	Instructions  string
	Logger        *slog.Logger
}

// New serves r.Tools(): one MCP tool per tool, with its name, description
// and input schema verbatim, whose handler is r.Call. A result's text is
// core.ToolResult.LLMText, and a failed call is an MCP error result.
func New(r *agentkit.Runner, opts Options) *Server

// ServeStdio serves one session on this process's stdin and stdout, for a
// harness that starts MCP servers as child processes, and returns when the
// client disconnects or ctx ends.
func (s *Server) ServeStdio(ctx context.Context) error

// ServeLoopback serves Streamable HTTP on 127.0.0.1 on a free port, behind
// a random bearer token every request must carry.
func (s *Server) ServeLoopback(ctx context.Context) (*Endpoint, error)

type Endpoint struct {
	URL   string
	Token string
}
func (e *Endpoint) Close() error
// ClaudeConfig is the JSON Claude Code's --mcp-config takes, naming the
// server name; PiConfig is Pi's .pi/mcp.json, with the tools exposed
// directly.
func (e *Endpoint) ClaudeConfig(name string) string
func (e *Endpoint) PiConfig(name string) string
```

`mcpserve` requires the root module (through `replace ../` in the
checkout, as the other nested modules do) and
`github.com/modelcontextprotocol/go-sdk`. It is added to `make`'s module
list (`MODULES`), to the module loop of `make doc`, and to
`internal/policy`'s gate modules. The stdio transport is the
SDK's; the HTTP transport is the SDK's `StreamableHTTPHandler` behind a
constant-time token check, as `examples/harness/server.go` does today.
*Spec time*: whether `ServeStdio` should also be a `cmd/agentkit-mcp`
command (a toolset is a program's, so probably not: the program that
builds the toolset serves it).

## 6. Migration

| Today | After |
|---|---|
| `tools.NewWorkspace(root)` | `workspace.Open(root, workspace.Options{})` |
| `ws.Root` | `ws.Root()` |
| `tools.All(tools.Options{Workspace: ws, Ignore: ig, …})` | `tools.For(ws, tools.Options{…})` with `ig` in `workspace.Open` |
| `tools.Walk(ctx, ws, sub, opts, fn)` | `ws.Walk(ctx, sub, opts, fn)` |
| `tools.IgnoreOptions`, `tools.NoGlobalExcludes()`, `tools.WalkOptions`, `tools.Normalize` | the same names in `workspace` |
| `ws.References(ctx, target, opts)` | `tools.References(ctx, ws, target, opts)` |
| `sandbox.LocalDir{Ignore: tools.IgnoreOptions}` | `sandbox.LocalDir{Ignore: workspace.IgnoreOptions}` |
| `agentkit.Config{Tools, Policy, Guard, After}` | `agentkit.Config{Toolset: agentkit.Toolset{Tools, Policy, Guard, After}}` |
| a hand-rolled pipeline (`examples/harness/server.go`) | `agentkit.NewRunner(ts)` and `Runner.Call` |
| `agent.ReachableTools()` | unchanged (also `agent.Runner().Reachable()`) |
| a `switch t.Name` over `tools.All` | `core.Named(ts, "read_file", …)` |
| `guard.Restricted(guard.Options{AllowedPrograms: ps})` | `guard.Allow(ps...)` (`Restricted` stays for the other options) |
| `faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall(id, n, a)}, StopReason: core.StopReasonToolUse}` | `faux.ToolCall(id, n, a)` |
| `p.Create`, `sb.Sync`, `sb.Tools` | `sandbox.Open(ctx, p, spec, src, opts)` (the three stay for a second `Sync`, or none) |
| an MCP server in the embedder | `mcpserve.New(runner, opts).ServeLoopback(ctx)` |

Inside this repository the move touches: `tools` (the files that move,
`For`, `References`), `codesearch` (72 `NewWorkspace` calls, 8 `Walk`, the
`Options` it builds), the sandbox tree (`internal/agent` opens the
workspace; `rsyncx` and `LocalDir` take `workspace.IgnoreOptions`;
`attach` builds `tools.For`), `prompt` (unchanged: it reads tool names and
guideline constants from `tools`), the root driver (`agent.go`,
`loop.go`, `batch.go`, `nested.go` reorganised under the runner), every
example, the README, `docs/architecture.md` and `docs/configuration.md`,
and `internal/policy`'s API golden and package-graph tests.

The README quickstart after:

```go
ws, err := workspace.Open(".", workspace.Options{})
ts, err := tools.For(ws, tools.Options{})
client, _, err := anthropic.Resolve(anthropic.OSEnv{})
agent, err := agentkit.New(agentkit.Config{
	Client: client, Model: "claude-opus-5-5",
	Toolset: agentkit.Toolset{Tools: ts, Guard: guard.Allow("go", "git")},
})
```

Code mode over four tools:

```go
bound, err := core.Named(ts, "list_files", "find_files", "read_file", "search_files")
cm, _, err := codemode.New(bound, codemode.Options{})
```

A sandbox served to another harness:

```go
sb, proxies, err := sandbox.Open(ctx, p, spec, sandbox.GitRepo{URL: repo, Depth: 1}, sandbox.ToolOptions{})
defer sb.Close(ctx)
cm, _, err := codemode.New(proxies, codemode.Options{})
runner, err := agentkit.NewRunner(agentkit.Toolset{Tools: append([]core.Tool{cm}, proxies...), Guard: guard.Allow("go", "gofmt", "git", "specr")})
ep, err := mcpserve.New(runner, mcpserve.Options{}).ServeLoopback(ctx)
defer ep.Close()
// exec claude -p … --tools "" --strict-mcp-config --mcp-config ep.ClaudeConfig("agentkit") …
```

## 7. Order of work

Five spec packages, each landing with its examples and documentation
updated, `make doc` run and `docs/generated/` committed (the steering
file's rule for any change to an exported identifier), `make check` green,
and the API golden regenerated where it changes:

1. **20 `workspace` package** (D3): the package, `tools.For`,
   `tools.References`, the moves in `codesearch` and the sandbox tree,
   `sandbox.LocalDir.Ignore`'s type. The biggest diff and no new concept;
   it goes first so the later specs are written against the final names.
2. **21 toolset and runner** (D1, D2): `Toolset`, `NewRunner`, `Runner`,
   the new `Config`, `Agent.Runner`, the driver reorganised under the
   runner; `examples/harness/server.go` loses its pipeline here.
3. **22 helpers** (D5): `core.Named`, `guard.Allow`, `faux.ToolCall`,
   `faux.Text`, `sandbox.Open`; the `codemode`, `sandbox` and `gateway`
   examples and the README rewritten on them.
4. **23 `mcpserve`** (D4): the module, both transports, the two config
   strings, the gate wiring; `examples/harness` rewritten on it, which
   deletes `server.go`.
5. **24 contract and documentation** (D6, D7): the exported-API golden and
   its erratum, the consumer-contract table in the README, the package
   graph and data-flow sections of `docs/architecture.md`, the
   configuration reference, and the measurement in §Intent redone over
   the rewritten examples as the record that the goal was met.

Specs 20 and 21 are independent of each other in code but 21's examples
assume 20's names, so 20 lands first. 22 depends on 21. 23 depends on 21.
24 depends on all of them.

## 8. Acceptance criteria

1. `agentkit.NewRunner(agentkit.Toolset{Tools: []core.Tool{cm}})`, with
   `cm` a `codemode.New` over `tools.For(ws, …)`, runs a script that calls
   `read_file` through `Runner.Call` with no `Agent`, and the nested call
   is reported to `Toolset.Events` as `ToolExecutionStart` and
   `ToolExecutionEnd` with the `code_mode` call as its parent.
2. `NewRunner` refuses what `New` refuses today, with the same errors:
   `core.ErrUnguardedExecute` for a reachable shell without a `Guard`, the
   cycle error, the terminating-through-wrapper error; `agentkit.New`
   refuses a `Config` whose `Toolset` fails `NewRunner` without sending a
   request.
3. `Agent.Run` over a `Toolset` with a `Guard` produces the same transcript
   and events as today for the root module's golden and scripted tests:
   the move under the runner changes no behaviour.
4. `workspace.Open(root, Options{Ignore: NoGlobalExcludes()})` refuses an
   escaping path in `Resolve`, walks what `tools.Walk` walks today, and
   `tools.For(ws, …)` returns the same eleven tools with the same
   definitions (the sandbox agent's definition digest is unchanged).
5. `tools.Options` has no `Workspace` and no `Ignore` field;
   `tools.NewWorkspace`, `tools.Walk`, `tools.IgnoreOptions` and
   `tools.All` do not exist.
6. `core.Named(ts, "read_file", "no_such_tool")` returns an error that
   names `no_such_tool` and lists the names in `ts`.
7. `sandbox.Open` on `local` with a `LocalDir` returns a live sandbox and
   the proxies; `sandbox.Open` whose `Sync` fails returns the error with
   the sandbox closed (the provider holds nothing afterwards).
8. `mcpserve.New(r, Options{}).ServeLoopback(ctx)` answers `tools/list` with
   one tool per `r.Tools()`, with the same `name`, `description` and
   `inputSchema` bytes `core.ToolWire` would send; a `tools/call` without
   the token is HTTP 401; a blocked call is an MCP result with `isError`
   and the guard's reason; AgentKit's own `mcp.Pool` connects to it and
   runs a `code_mode` call end to end.
9. `ServeStdio` serves the same tools to the SDK's stdio client over pipes
   in a test.
10. The four examples in §Intent measure, after the rewrite, at most one
    line for each of the first four chores and zero for the fifth;
    `examples/harness` has no `server.go`.
11. `make check` passes in every module, including `mcpserve`;
    `internal/policy`'s no-listen check still passes for the root and the
    sandbox modules and is not extended to `mcpserve`; the cross-target
    gate covers `mcpserve`.
12. The API golden is regenerated once, in spec 24, with an erratum in
    `docs/errata/13_sandbox_core.md` that names this PRD.

## 9. What is given up, stated plainly

- **Every embedder on the current API changes.** `Config`'s four tool
  fields, `tools.All`, `tools.NewWorkspace` and `ws.Root` are the
  identifiers every program on AgentKit starts with, and all of them move.
  Inside this repository that is 72 `NewWorkspace` calls in `codesearch`
  alone. The migration is mechanical and the table in §6 is complete, but
  it is a migration.
- **Spec 13's "exported API unchanged" invariant is retired.** The golden
  stays as a change detector; it no longer pins a frozen surface.
- **One more package to know** (`workspace`) and one more module
  (`mcpserve`). The package replaces a type a reader had to find inside
  `tools`; the module is opt-in.
- **`Runner` emits to a function, not a stream.** An embedder that wants
  the ordering guarantees of `core.EventStream` for calls outside a run
  builds the stream and hands its `Push` in. The agent keeps its stream.
- **The simplest quickstart does not get shorter**: five statements before,
  five after. What it gains is that each statement names one thing — a
  workspace, its tools, a client, a toolset, an agent — and that the
  fourth is reusable without the fifth.

## Dependencies

No new dependency. `workspace` takes `bmatcuk/doublestar` with the ignore
engine; `mcpserve` requires `github.com/modelcontextprotocol/go-sdk`, which
the root already requires for the client. `docs/DEPS.md` gains the
`mcpserve` row.

## Documentation

| Change | Update |
|---|---|
| any exported identifier | `make doc`, and `docs/generated/` committed |
| `workspace` package, `tools.For`, `tools.References` | `docs/api.md` (a row for `workspace`); `README.md` quickstart and packages table; `docs/architecture.md` package graph, "Symbol and reference navigation"; `docs/configuration.md` tool options |
| `Toolset`, `Runner`, the new `Config` | `docs/api.md` (the root row); `README.md` consumer contract; `docs/architecture.md` data flow, nested calls, code mode (the paragraph on `ErrNoNestedCaller`), invariants; `docs/configuration.md` `agentkit.Config` |
| `mcpserve` | `docs/api.md` (a nested-module row); `README.md` (a section beside the gateway's); `docs/architecture.md` shape and package graph; `docs/cli.md` only if a command is added; `docs/DEPS.md` |
| helpers | `README.md`, `examples/README.md`, each example's `README.md` |
| the retired API freeze | `docs/errata/13_sandbox_core.md` |
| `make` module list, gates | `README.md` make targets, `docs/architecture.md` testing layout, `AGENTS.md` repository layout |
