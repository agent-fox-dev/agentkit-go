# Cut AgentKit down to the hands and one driver

Status: **proposed**. Revised after PRD 08 (scripts, specs 06 to 08) and
PRD 09 (libraries) landed; both stand and this PRD builds on them.
Supersedes `agent-kit-prd.md` (0.4.2) as the statement of what this module
is for, PRD 03 (the MCP client, now delivered by PRD 09 step 1), and most of
PRD 06 (multi-phase pipelines). PRDs 04, 05 and 07 stand. The agent-fox
side of this decision, the `af` toolbelt, is its own PRD in that
repository.

This document was first filed as PRD 08 and renumbered when it collided
with the scripts PRD of the same number.

## Intent

AgentKit was specified as a general, dependency-free agent SDK: five wire
APIs, MCP in both directions, plugins, skills, sessions, compaction,
delegation, a differential harness. It is 65 000 lines of production Go and
69 000 of tests, with one consumer, agent-fox, which uses a fraction of it
and is about to stop owning the agent loop.

PRD 09 already made the first cut the right way: it dropped the
standard-library and cgo rules, put MCP on the official SDK, the JSON
boundary on `encoding/json/v2`, outlines on tree-sitter, and deleted the
hand-rolled code those replaced. PRD 08 added the one capability that
changes what a tool set costs: code mode, where a model-written Starlark
script runs over bound tools and only its output reaches the context. Both
stay in full.

This PRD finishes the job. AgentKit becomes two things:

1. **The hands.** A library of workspace mechanics that any agent, in any
   harness, can use: path containment, the ignore-aware walk, the
   subprocess runner, the file and search tools, outlines, symbols and
   references, the indexed search module, the MCP tool pool, code mode,
   and the shell guard as a pure function.
2. **One driver.** A minimal loop over the official Anthropic Go SDK for an
   embedder that runs an agent unattended and does not want to depend on a
   vendor CLI. One provider, the nested-call pipeline that code mode needs,
   no plugins, no sessions, no summarization. About 2 000 lines.

Everything else is deleted, not deprecated. There is no compatibility layer
and no migration period: a tag marks the last full build, agent-fox pins it
until the `af` work lands, and the next tag is the cut.

The decision rule, applied to every package: **is this here because the
model is bad at judgment, or because we chose to own the whole stack?** The
first kind stays. The second kind goes.

## Goals

1. The root module is under 20 000 lines of production Go, from 65 000,
   and every line of it is exercised by agent-fox, by code mode, or by the
   driver.
2. The module is served by the Go module proxy under its repository path
   and consumed by version, with no `replace`.
3. The wire is the official SDK. Thinking, effort, caching, strict tools,
   Vertex and Bedrock are the SDK's problem, tracked by Anthropic, not
   re-implemented here.
4. Code mode, nested calls and output schemas (specs 06 to 08) keep every
   behaviour their specs pin, on the smaller driver.
5. Every mechanism that stays has a single consumer-facing entry point and
   a test that drives it through the real code path with the scripted
   provider.
6. The documentation is short enough to be true. One README under 200
   lines, one architecture page, one configuration page, the dependency
   ledger. No table claims a test exists that does not.

## Non-goals

- Multi-vendor support. agent-fox's tiers resolve to Claude models; the
  other four wires have no user. An embedder that needs another vendor
  writes a `core.ProviderClient`; the interface stays one method and the
  faux provider is the template.
- Interactive agents: mid-run steering, follow-up queues, resume from disk.
  The driver runs one prompt to a stop condition.
- A pipeline, phase or spec abstraction. Still agent-fox's.
- An MCP server. The `af` tools are served from agent-fox on the official
  SDK; nothing in this module listens on a socket.
- Backwards compatibility with 0.4.x. The last 0.4 build is tagged and
  frozen; nothing below promises to keep its API.

## 1. What stays, what changes, what goes

### Keep

| Package | Why | What changes inside it |
|---|---|---|
| `tools` | The hands. Workspace containment, `Walk`, the layered ignore engine, the file tools, the native search, `find_files`, `list_files`, `file_outline`, `find_symbol`, `find_references`, the extended subprocess runner, `ReducedEnv`, spill, the output schemas of spec 06. agent-fox imports 15 identifiers from here today and the `af` toolbelt will import more. | `fetch.go` and `ssrf.go` deleted with their two modules (`x/net/html`, `code.dny.dev/ssrf`): no coding pipeline fetches URLs and nobody grants the tool. `powershell.go` deleted: the "platform-stable tool list for the cache" argument only held when AgentKit owned every embedder's tool list. `proc_windows.go` stays so the runner cross-compiles. |
| `outline` | Tree-sitter under `cgo`, `go/ast` for Go, `none` without cgo (PRD 09 step 3). Repository map, structural checks, symbols and references all read it. | Unchanged. |
| `codesearch` (own module) | Opt-in indexed search; the separate module already isolates zoekt. | Unchanged and **frozen** until the navigation baseline in agent-fox is measured. ADR 07 there made it conditional on a number nobody has taken. |
| `codemode` | PRD 08's code-mode tool on `go.starlark.net`: the generated description, keyword bindings, `parallel`, the four limits, middle truncation and spill, the error values. The one feature in this module that lowers what an agent-fox phase costs. | Unchanged. Its dependency on `core.CallNested` is why the nested pipeline stays in the driver (§2). |
| `mcp` | After PRD 09 step 1 this is 2 100 lines over the official SDK: the pool that adapts server tools to `core.Tool` with qualified names, the collision check, schema and output-schema conversion, `structuredContent` pass-through (spec 06), config with `${VAR}` interpolation, the reduced subprocess environment, respawn, the result cap. Code mode binds MCP tools and the offline example proves it. | **Client and pool only.** `server.go`, `serve.go`, the API-key middleware, the sampling gate and `examples/mcpserver` are deleted. The strict-JSON wrappers at the stdio and HTTP boundaries stay. |
| `wire` | 644 lines on `encoding/json/v2` after PRD 09 step 2; the MCP client's strict boundary. | Kept as `mcp`'s dependency and deleted the day `mcp` stops needing it. `Bind`, which has no production caller, goes now. |
| `guard` | The one program-side check a host hook can call. | Reshaped as a pure function, `guard.Check(argv []string, opts Options) Decision`, with the `BeforeToolCall` adapter kept as a one-liner over it. Documented as best-effort: a floor, not a sandbox. |
| `schema` | agent-fox builds every submit-tool schema with its combinators; code mode renders tool signatures from `InputSchema` and `OutputSchema`; `core.PrepareArguments` validates every call, direct and nested, against the tool's schema. | Unchanged in role. `jsonx` folds in as `schema.Parse([]byte)`, an order-preserving decoder on `jsontext`, so a JSON Schema document becomes a `*Schema` without losing property order. Issue 87's validator gaps (`additionalProperties: false`, `enum`, bounds) are its backlog, and they matter more now: a script's arguments are validated here, not by the API. |
| `core` | The vocabulary: messages, blocks, tools, results, events, usage, the reachability and nested-call seams of spec 07. | Shrinks from 3 300 lines and 124 types to about 1 600 lines (§3). |
| `provider/anthropic` | The one wire. | Rewritten over `github.com/anthropics/anthropic-sdk-go`: encode the canonical request, decode the SDK's response and stream events. Vertex via `vertex.WithGoogleAuth`, Bedrock via the Mantle client. The hand-rolled transport, SSE decoder, retry ladder, credential store and OAuth refresh are deleted; the SDK owns them. §4 says why this reverses PRD 09 §4's ruling. |
| `provider/faux` | The scripted provider every test and agent-fox's whole suite runs against. | Unchanged in behaviour. |
| `catalog` | Model metadata the driver needs: context window, output cap, price, which thinking parameter the model takes. | Shrinks to a table of Claude rows and `catalog.Lookup(id)`. An unknown id is served with a warning and no price, not refused. Sibling cloning, tier aliases and clamping leave; agent-fox's tier table stays in agent-fox. |
| `prompt` | Base instructions plus per-tool guidelines, first-seen order, deduplicated. | Skill and project-context blocks deleted with the `skills` package. |
| `internal/toml` | PRD 09's 150-line adapter over `go-toml/v2`. | Kept for `mcp/config.go`, its one remaining caller after `skills` and `plugins` go. |
| root (`agentkit`) | The driver. | Rebuilt around what stays of the loop (§2). |

### Delete

| Package or file | Lines | Why it goes |
|---|---|---|
| `provider/openai`, `provider/openairesponses`, `provider/google`, `provider/ollama`, `provider` (shared transport, repair, salvage, credentials) | 9 100 | No user. The conformance suite, the three tool-result shapes, positional id synthesis and the cached-token netting table were real work solving a problem nobody here has. The findings are kept in `docs/archive/` so they are not rediscovered. |
| `mcp/server.go`, `mcp/serve.go`, `examples/mcpserver` | 500 | The `af` server lives in agent-fox. |
| `plugins`, `cmd/validate-plugins` | 1 200 | Four categories, a registry, a lint, and no plugin. |
| `skills`, `_skills/code-review`, `skillsconfig.go` | 2 300 | The only built-in skill is a Go review of this repository offered to every project (issue 91). agent-fox renders `AGENTS.md` into its own prompts and needs no trust gate from the SDK. |
| `session`, `resume.go` | 2 700 | Nothing resumes a phase. agent-fox writes its own events file. Spec 07's rule that nested calls are not logged becomes moot. The repair pass shrinks to the one rule a single-process run needs (§2). |
| `subagent` | 340 | Delegation is the embedder's loop calling the driver twice. A subagent that needs to be a tool is bound like any tool; nothing special-cases it. |
| `compaction` | 720 | Summarization is a second model round trip with its own budget, its own failure modes (issues 74, 84) and a cache-invalidating edit. Replaced by `Prune` (§2). |
| `middleware`, `stop` | 860 | Retry is the SDK's `max_retries`. Budget, turns and duration become three config fields. Caching, tracing and rate limiting (`x/time/rate` goes with it) had no consumer. |
| `imagex`, `images.go`, `x/image` | 400 | No coding pipeline sends images. `read_file` on an image returns an error naming the format, and its image output schema goes. |
| `jsonx` | 360 | Folded into `schema.Parse`. |
| `difftest` (own module) | 920 | Reports DARK by design and will stay dark. |
| `deferred.go`, `audit.go`, `core/audit.go`, `core/trace.go`, `core/plugin.go` | 500 | Deferred requests were never polled (issue 80). The hashed audit trail and the tracer duplicate the event stream, which spec 07 already made carry the parent tool-use id. |
| `examples/*` except `agentdemo`, `codingagent`, `customtools`, `codemode`, `mcp` | 11 000 | `cleaner`, `flatline` and `triage` are copies of agent-fox pipelines and drift from them; the rest demonstrate deleted features. The Makefile's `build-examples` target goes with them. |
| `perf_budget_test.go` and every wall-clock threshold test | | They fail under load (issue 92; two failed on a clean run while this PRD was first written). What NFR-PERF-03 pins, the count of schema serializations, stays as a count. |

Roughly 32 000 lines of production code and 40 000 of tests leave the
repository. The module keeps about 19 000 production lines plus the
`codesearch` module's 3 400.

## 2. The driver

What survives of the loop is the part that is right and not in the SDK's
tool runner: the batch semantics, the nested-call pipeline, the events, the
bounds, and ending a run on a tool's say-so. The driver does not use
`BetaToolRunner`, because those five things are the reason it exists; it
calls `Messages.New` and `Messages.NewStreaming` directly.

```go
type Config struct {
    Client     *anthropic.Client   // built by anthropic.Resolve or the caller
    Model      string
    Effort     Effort              // low | medium | high | xhigh | max; maps to output_config.effort
    System     string
    Prefix     []core.Message      // sent after system on every request, with a cache breakpoint
    Tools      []core.Tool
    Policy     core.ToolPolicy     // resolved through wrappers, as spec 07 defines
    Guard      core.BeforeToolCall // required when any shell tool is reachable (ErrUnguardedExecute)
    After      core.AfterToolCall
    MaxTurns   int
    MaxCostUSD float64
    Timeout    time.Duration
    Prune      PruneOptions        // zero value: off
    MaxTokens  int
}

func New(cfg Config) (*Agent, error)
func (a *Agent) Run(ctx context.Context, prompt string) (core.RunResult, error)
func (a *Agent) Stream(ctx context.Context, prompt string) (*core.EventStream, error)
func (a *Agent) Messages() core.Messages
func (a *Agent) Usage() core.Usage
func (a *Agent) ReachableTools() []core.Tool
```

**Kept, with their tests:**

- Iteration on the presence of `tool_use` blocks, never on `stop_reason`.
- One `ToolResultMessage` per call; the wire coalesces.
- The batch executor: the abort decision made once before any handler
  starts, a result for every call, true concurrency without `errgroup`,
  the batch-scoped finalize mutex.
- **The nested-call pipeline of spec 07, whole.** `New` refuses a
  reachability cycle and a wrapper that reaches a `Terminating` tool;
  `ToolPolicy.Resolve` filters through wrappers and drops an emptied one;
  the unguarded-shell check counts a shell reached through a wrapper; a
  wrapper's handler gets a `NestedCaller` on its context; every nested
  call runs prepare, validate, `BeforeToolCall`, the handler,
  `AfterToolCall` and the execution events with `ParentToolUseID`, in that
  order; a blocked nested call is an error result, not a Go error; an
  interceptor's terminate vote ends the wrapper and an unmarked handler's
  is ignored and annotated; nested calls issued together run concurrently
  in issue order; cancellation aborts them; nested spend reports as it
  happens. What the pipeline loses with the deletions is the audit record
  and the tracing span, which the events already carry.
- `max_tokens` with tool calls executes none of them and answers each with
  the fixed synthetic result. This is the one repair rule that stays; the
  other six existed for resumed and damaged transcripts.
- `ToolResult.Terminate` ends the run after the batch; a rejected call does
  not. `RunStopToolTerminate`, `RunStopMaxTurns`, `RunStopBudgetExceeded`,
  `RunStopTimeout`, `RunStopAborted`, `RunStopRefusal`, `RunStopError`.
- Tool results reach the model as text; `Data` stays for interceptors,
  nested callers and code mode.
- Tool-call argument bytes are replayed unchanged (`json.RawMessage` end
  to end), pinned by the golden request test.
- Every panic in an interceptor or handler is contained and reported.
- The event stream never blocks on its consumer.

**Added:**

- `Prefix` and its cache breakpoint. The SDK places `cache_control`; the
  driver stamps the last system block, the last tool, the last prefix
  block, and a rolling breakpoint on the last user message. This is PRD 06
  §3, the one part of that PRD agent-fox's token problem needs from the
  SDK.
- `Prune`: at a threshold fraction of the context window, tool results
  older than `KeepTurns` are replaced in the outbound view by one line
  naming the call and its size. Pairing with the `tool_use` block is kept.
  The transcript on `Messages()` is never edited; only the view sent is.
  Code mode and pruning are complementary: one keeps intermediate results
  out of the context, the other ages out the ones that got in.
- `Effort` replaces `ThinkingLevel`. On every current Claude model the
  parameter is `output_config.effort` with adaptive thinking; `budget_tokens`
  is a 400. The catalog row says which models still take a budget, and
  the encoder uses it for those only.
- Every tool is declared `strict: true`. The API then guarantees the
  model's arguments match the schema. `core.PrepareArguments` still runs
  on every call, because a nested call's arguments come from a script, not
  the API, and the validator is the only thing checking them.
- **No forced tool choice.** Fable 5.1, Opus 5.5 and Sonnet 5.5 reject
  `tool_choice: any` and `tool_choice: tool` with a 400. PRD 06 goal 6 ("a
  phase can require its terminating tool on a given turn") is withdrawn,
  not deferred: the mechanism does not exist on the models in use. A phase
  that ends without its terminator ends `RunStopEndTurn` with no result,
  and the embedder's prompt is the only lever.

**Dropped:** steering, follow-up, `Continue`, deferred requests and
redemption, the plugin, audit and tracer hooks, `SetModel` mid-run, the
history tree and branching, `ConstrainJSONSchema` and `StrictPrefer`
(strict is unconditional), the middleware chain, the summarizers.

## 3. `core` after the cut

| Stays | Goes |
|---|---|
| `Message`, `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `Messages`, `Role` | `ConversationHistory`, `SnapshotBranch`, the tree |
| `ContentBlock`: `TextBlock`, `ThinkingBlock`, `ToolUseBlock` | `ImageBlock` and document blocks |
| `Tool` with `InputSchema`, `OutputSchema`, `ReachableTools`, `Terminating`, `ExecutionMode`, `PromptGuidelines`; `ToolWire` | `ConstrainedSampling` on the wire projection |
| `ToolResult`, `OKResult`, `ErrResult`, `BlockErrorCode`, `ToolMetadata` | |
| `PrepareArguments`, `PreparedArguments` | |
| `ToolPolicy` and its recursive `Resolve`; `ReachableTools(tools)` | `MCPServerOf` |
| `NestedCaller`, `WithNestedCaller`, `CallNested`, `ErrNoNestedCaller`, `ErrTerminated` | |
| `BeforeToolCall`, `BeforeToolCallContext` (with `ParentToolUseID`, `ParentToolName`), `BeforeToolCallDecision`, `AfterToolCall`, `AfterToolCallContext`, `ErrUnguardedExecute` | `Plugin`, `EventHookPlugin`, `Tracer`, `AuditEvent` |
| `WithUsageReporter` | |
| `ProviderClient` (one method: `Stream(ctx, Request) (<-chan StreamEvent, error)`), `Request`, `StreamEvent` | `ProviderRegistry`, `ProviderStreamOptions`, `ClientFunc`, `Middleware` |
| `Event` and the run, turn, message, text, tool-call and tool-execution events (with `ParentToolUseID`); `EventStream`; `MarshalEvent` | session-start and session-end audit events |
| `Usage` (input, output, cache read, cache write, requests, cost), `RunResult`, `RunStopReason`, `StopReason` | `ThinkingLevel` and its order (replaced by `Effort`) |
| `Model` (id, context window, max output, prices, thinking kind) | `Model.API`, `Model.Provider` |

Target: about 1 600 lines, under 50 exported types.

## 4. Rules that change

**The module path is the repository path.** `go.mod` becomes
`module github.com/agent-fox-dev/agentkit-go` and the proxy serves it.
agent-fox deletes its two `replace` directives and pins a tag. The
`codesearch` module follows.

**The dependency ledger is an allowlist test.** PRD 09 already allows
third-party modules and cgo behind build tags with a pure-Go fallback;
this PRD does not reopen that. After the cut the root module's direct
dependencies are `anthropic-sdk-go`, `go.starlark.net`,
`modelcontextprotocol/go-sdk`, `go-tree-sitter` with its grammar modules,
`doublestar` and `go-toml/v2`. The allowlist test in `internal/policy`
names each with its one-sentence reason and fails on any addition not
named; `docs/DEPS.md` keeps the longer rulings. The cross-target
`CGO_ENABLED=0` gate and the host `CGO_ENABLED=1` gate from PRD 09 stay.

**The Anthropic wire moves to the SDK, reversing PRD 09 §4.** That ruling
rejected the vendor SDKs because they save only 15 to 25% while transcript
conversion, repair, cache stamping, compat flags, thinking mapping and
usage netting stay, and because they break REQ-SEC-12.6, REQ-PROV-13 and
REQ-PROV-18. Every one of those reasons was about five wires. With one:
there is no cross-wire transcript conversion, no compat flag, no netting
table; repair is one rule; thinking mapping is a field named `effort`;
cache stamping is a field the SDK exposes. The three requirements are
from the superseded PRD, and this one drops them deliberately. Rejecting
a duplicate `stop_reason` in a TLS-authenticated vendor response guards a
threat this module does not face. Abandoning on a long `Retry-After` is a
budget question, answered by `MaxCostUSD` and `Timeout`. `OnPayload` has
no consumer. The golden request test is rewritten once, for one wire, and
is the check that an SDK update did not move the cache prefix.

**The SDK is pinned to a minor version** and moved deliberately.

**No wall-clock thresholds in tests.** A budget is a count or a structural
pin (a test that deadlocks on the wrong implementation), never a duration.

**The README names no test.** Mutation verification continues as a
practice recorded in the PR that did it, not as a table that drifts.

**No model identifier, vendor pricing or thinking rule lives outside
`catalog`**, and `catalog` is the only file that changes when Anthropic
ships a model.

## 5. What agent-fox sees

Every identifier agent-fox imports today, and its fate. This is the
consumer contract the cut is checked against.

| Today | After |
|---|---|
| `agentkit.Agent`, `NewAgentWithHistory`, `DefaultProviders` | `agentkit.New(Config)`; history is `Config.Prefix` plus the run's own messages; there is no registry |
| `core.AgentConfig` (31 fields) | `agentkit.Config` (14 fields) |
| `core.Middleware`, `ClientFunc`, `middleware.Retry` | gone; `anthropic.Resolve` sets the SDK's retries and honours `Retry-After` |
| `core.ToolPolicy` | unchanged, resolved through wrappers |
| `core.ProviderRegistry`, `ProviderStreamOptions`, `ConversationHistory` | gone |
| `core.ConstrainJSONSchema`, `ConstrainedSampling`, `StrictPrefer` | gone; strict is unconditional |
| `core.ThinkingLevel` and the six levels, `catalog.ClampThinkingLevel` | `agentkit.Effort` with five values; the catalog row says whether the model takes it |
| `core.RunStopPolicy` | `RunStopMaxTurns`, `RunStopBudgetExceeded`, `RunStopTimeout` |
| `stop.AfterTurns`, `OverBudget`, `WhenToolCalled`, `Any`, `Error` | `Config.MaxTurns`, `MaxCostUSD`, `Timeout`; `ToolResult.Terminate` |
| `compaction.NewContextTransform`, `Deps`, `ModelSummarizer`, `ModelTurnSummarizer`, `Summarization` | `Config.Prune` |
| `provider.ResolveAuth`, `VendorAuth`, `CredentialNone`, `Env`, `Calls` | `anthropic.Resolve(env) (*anthropic.Client, Source, error)`: API key, Vertex when `CLAUDE_CODE_USE_VERTEX` is set, Bedrock when `CLAUDE_CODE_USE_BEDROCK` is set. Bedrock stops being refused. Request counts are in `Usage.Requests`. |
| `provider/openai`, `google`, `ollama`, `openairesponses` | gone; agent-fox's tier table loses its non-Claude rows and its tests of them |
| `prompt.Build`, `Input`, `SkillBlocks` | `prompt.Build(system string, tools []core.Tool) string` |
| `skills.ConfigFor`, `Discover`, `DiscoverContext` | gone; agent-fox already reads `AGENTS.md` and `.specs/steering.md` itself |
| `catalog.ResolveModel`, `ErrUnresolvedModel` | `catalog.Lookup(id) (core.Model, bool)` |
| `jsonx.DecodeOrdered` and the `Ordered*` types | `schema.Parse` |
| `schema.Object`, `Prop`, `Required`, `Enum`, `Array`, `String`, `Opt`, `AdditionalProperties`, `Result`, `Type*` | unchanged |
| `tools.*` (15 identifiers), `outline.*`, `codesearch.*`, `guard.Options`, `guard.Restricted` | unchanged; `guard.Check` added |
| `core` messages, blocks, events, `Tool`, `ToolResult`, `ErrResult`, `OKResult`, `BeforeToolCall*`, `ErrUnguardedExecute`, `Usage`, `RunResult` | unchanged |
| not yet imported: `codemode.New`, `core.ReachableTools`, `mcp.NewPool` | available; which phases bind code mode is agent-fox's PRD |

About two thirds of the identifiers agent-fox uses are untouched. The rest
are the loop's configuration surface, which agent-fox's `agentrun` package
wraps in one place and which the `af` work replaces.

## 6. What this cancels

- **PRD 03** (classic MCP client): delivered by PRD 09 step 1 and closed.
- **PRD 06**: §3 (stable prefix) and §4 (pruning) land here in §2; §5 and
  §7 (honest usage, guidelines under a custom prompt) become trivially
  true; §6 (the runner) shipped as spec 04 and stays; §8 (symbols) stays
  as PRD 04/07 work; §9 (stdlib) was reversed by PRD 09; §10 (every text a
  bundled document) and §12 are withdrawn. The driver has no prompt text
  beyond the base instructions, the guidelines and code mode's generated
  description, and agent-fox owns its own. Goal 6 (require the
  terminator) is withdrawn as impossible on current models.
- **PRD 09 §4, the provider row only**, is reversed for the reasons in §4
  above. Every other ruling in that table stands.
- **`agent-kit-prd.md` 0.4.2** moves to `docs/archive/` as history. Its
  requirement ids stop being cited in new code and tests.
- **`docs/api.md`** (the MCP server's surface) and **`docs/cli.md`**
  (`validate-plugins`, `difftest`, example flags) are deleted. The errata
  that describe deleted packages go with them.
- Open issue 91 closes by deletion; 92 closes with the README rewrite.

## 7. Order of work

Each step is one pull request and leaves `make check` green in this
repository. agent-fox stays on the frozen tag until step 6 and is not
rebuilt against intermediate steps.

1. **Freeze.** Tag `main` as `v0.4.3`, the last full build with PRDs 08
   and 09 in it. agent-fox pins it by `replace` to that checkout, which it
   already does in effect.
2. **The cut.** Delete the packages, files and examples in §1. Rename the
   module. Replace PRD 09's dependency tests with the allowlist test.
   Delete the wall-clock tests. The root tests that drove deleted features
   go with them; the ones that pin kept behaviour, the spec 06 to 08
   suites included, are kept and must still pass. This is the largest diff
   and the most mechanical; it ships first so every later step is reviewed
   against a small tree.
3. **The wire.** Rewrite `provider/anthropic` over the SDK.
   `anthropic.Resolve`. `catalog.Lookup`. `Effort`. Strict tools. The
   golden request test is rewritten for the new encoder and is the
   acceptance test.
4. **The driver.** `agentkit.Config` and `New`. Fold bounds into config.
   Delete steering, `Continue`, deferred, the audit and tracer calls from
   `batch.go` and `nested.go`. Add `Prefix` stamping and `Prune`. The
   scripted-provider tests in §8 and the existing spec 07 and 08 smoke
   tests are the acceptance test.
5. **`core`, `schema`, `guard`.** The §3 shrink, `schema.Parse`,
   `guard.Check`.
6. **Docs and tag.** README, `architecture.md`, `configuration.md`,
   `DEPS.md` rewritten; `agent-kit-prd.md` archived; the rest deleted. Tag
   `v0.5.0`. agent-fox moves to the tag, drops `replace`, deletes its
   non-Claude provider tests and its `compaction`/`stop`/`middleware`
   wiring, in its own PR under its own PRD.

Steps 3, 4 and 5 are independent of each other once step 2 has landed and
may be worked in parallel.

## 8. Acceptance criteria

- `git ls-files '*.go' | grep -v _test | xargs wc -l` over the root module
  reports under 20 000 lines; under 30 000 for tests.
- `go list -m all` in the root module lists only the modules the allowlist
  test names and their requirements; the test fails on any addition not
  named.
- No directory named `plugins`, `skills`, `session`, `subagent`,
  `difftest`, `imagex`, `compaction`, `middleware`, `stop` or `jsonx`
  exists; `tools/fetch.go`, `tools/ssrf.go`, `tools/powershell.go`,
  `mcp/server.go` and `mcp/serve.go` do not exist; `codemode/`, `mcp/pool.go`
  and `nested.go` do.
- `go get github.com/agent-fox-dev/agentkit-go@v0.5.0` resolves through the
  public proxy; agent-fox at its matching commit builds with no `replace`
  directive in `go.mod`.
- `grep -rn 'time.Since\|Elapsed' --include='*_test.go'` finds no test that
  compares a duration against a threshold to decide pass or fail.
- Every test in `codemode/`, the spec 06 conformance suite, and the spec 07
  smoke tests at the root pass unchanged except where a deleted hook
  (audit, tracer, session log) was the thing asserted; those assertions
  are removed, not weakened.
- The following run against `provider/faux` through `agentkit.New`: a
  three-call batch runs its handlers concurrently and returns three
  results; a cancellation mid-batch runs zero or three handlers, never
  one; a `max_tokens` turn with a tool call executes nothing and answers
  it with the fixed text; a `Terminate` result ends the run after its
  batch while a rejected call does not; a code-mode script calling a tool
  the interceptor blocks receives an error value and the wrapper completes;
  `Prune` elides a result older than `KeepTurns` from the outbound view and
  leaves `Messages()` intact; `MaxCostUSD` stops the run with
  `RunStopBudgetExceeded` before the request that would exceed it; a shell
  tool reachable through code mode with a nil `Guard` is refused by `New`.
- The golden Anthropic request shows: `cache_control` on the last system
  block, the last tool, the last prefix block and the last user block;
  `strict: true` on every tool; `thinking: {type: adaptive}` and
  `output_config.effort` for a current model and no `budget_tokens`; a
  replayed `tool_use` input byte-identical to the one decoded; no
  `outputSchema`, `reachable_tools` or `terminating` field anywhere in the
  body.
- `anthropic.Resolve` returns a Vertex-configured client when
  `CLAUDE_CODE_USE_VERTEX` is set and a Bedrock one when
  `CLAUDE_CODE_USE_BEDROCK` is set, pinned by a test that inspects the
  client's base URL and auth source without a network.
- `README.md` is under 200 lines and names no test function.
- Issues 91 and 92 are closed.

## 9. What is given up, stated plainly

- **Other vendors.** An embedder on OpenAI, Gemini or Ollama writes a
  provider. The interface is one method and the faux provider is the
  template.
- **The MCP server.** A server for the `af` tools lives in agent-fox on
  the official SDK. This module only consumes MCP tools, through the pool,
  directly or from a script.
- **Resume.** A run that dies is re-run. agent-fox already treats every
  phase as fresh and parks work in git, which is the durable state.
- **Summarization.** A transcript that outgrows the window after pruning
  ends the run with `RunStopError` naming the window. Pruning at 35% of
  the window, code mode keeping intermediate results out of the context,
  and the 1M-token window of current models make this a configuration
  error, not an operating condition.
- **The audit trail and tracer.** The event stream carries every tool
  call, nested ones with their parent id. An embedder that wants a hashed
  audit log writes one from the events.
- **Control over the wire bytes.** The SDK marshals the request. Cache
  stability is verified by the golden test and by `Usage.CacheRead`, not
  by owning the encoder, which is why the SDK is pinned.

## Documentation

- `README.md`: what the two things are, the ten-line driver example, the
  code-mode example, the consumer contract table from §5 reduced to the
  kept identifiers.
- `docs/architecture.md`: the package graph of §1 and the import rule
  (everything imports `core`; `tools` imports `outline` and `schema`;
  `codemode` and `mcp` import `core` and `schema`; the root imports
  `tools`, `prompt`, `catalog` and `provider/anthropic`; nothing imports
  the root).
- `docs/configuration.md`: `Config`, `anthropic.Resolve` and the three
  environment variables it reads, `PruneOptions`, `codemode.Options`,
  `mcp.Config`, the catalog table.
- `docs/DEPS.md`: one ruling per module on the allowlist, including the
  SDK's, with PRD 09's rejected candidates kept as they are.
- `docs/archive/agent-kit-prd.md`: the 0.4.2 PRD, unchanged, with a
  one-line header pointing here, and beside it `docs/archive/wires.md`,
  the findings from the deleted providers and conformance suite worth not
  rediscovering.
