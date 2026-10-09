# Cut AgentKit down to the hands and one driver

Status: **proposed**. Supersedes `agent-kit-prd.md` (0.4.2) as the
statement of what this module is for, and PRD 03 (the MCP client) and most
of PRD 06 (multi-phase pipelines) as roadmap. PRDs 04, 05 and 07 (symbol
navigation, indexed search, `find_references`) stand. The agent-fox side of
this decision, the `af` toolbelt, is its own PRD in that repository.

## Intent

AgentKit was specified as a general, dependency-free agent SDK: five wire
APIs, MCP in both directions, plugins, skills, sessions, compaction,
delegation, a differential harness. One month in, it is 65 000 lines of
production Go and 68 000 of tests, with one consumer, agent-fox, which uses
a fraction of it and is itself about to stop owning the agent loop.

This PRD makes the module small enough for one person to own and shaped
for what agent-fox actually needs from it. AgentKit becomes two things:

1. **The hands.** A library of workspace mechanics that any agent, in any
   harness, can use: path containment, the ignore-aware walk, the
   subprocess runner, the file and search tools, outlines and symbols, the
   indexed search module, and the shell guard as a pure function.
2. **One driver.** A minimal loop over the official Anthropic Go SDK, for an
   embedder that needs to run an agent unattended and does not want to
   depend on a vendor CLI. One provider, no plugins, no sessions, no
   summarization. About 1 500 lines.

Everything else is deleted, not deprecated. There is no compatibility
layer and no migration period: a tag marks the last full build, agent-fox
pins it until the `af` work lands, and the next tag is the cut.

The decision rule, applied to every package: **is this here because the
model is bad at judgment, or because we chose to own the whole stack?**
The first kind stays. The second kind goes.

## Goals

1. The root module is under 15 000 lines of production Go, from 65 000,
   and every line of it is exercised by agent-fox or by the driver.
2. The module is served by the Go module proxy under its repository path
   and consumed by version, with no `replace`.
3. The wire is the official SDK. Thinking, effort, caching, strict tools,
   Vertex and Bedrock are the SDK's problem, tracked by Anthropic, not
   re-implemented here.
4. Every mechanism that stays has a single consumer-facing entry point and
   a test that drives it through the real code path with the scripted
   provider.
5. The documentation is short enough to be true. One README under 200
   lines, one architecture page, one configuration page. No table claims a
   test exists that does not.

## Non-goals

- Multi-vendor support. agent-fox's tiers resolve to Claude models; the
  other four wires have no user. An embedder that needs another vendor
  writes its own `core.ProviderClient`, and the interface stays small
  enough that this is a day's work.
- Interactive agents: mid-run steering, follow-up queues, resume from disk.
  The driver runs one prompt to a stop condition. A chat UI is a different
  product with a different loop.
- A pipeline, phase or spec abstraction. Still agent-fox's.
- Backwards compatibility with 0.4.x. The last 0.4 build is tagged and
  frozen; nothing below promises to keep its API.

## 1. What stays, what changes, what goes

### Keep

| Package | Why | What changes inside it |
|---|---|---|
| `tools` | The hands. Workspace containment, `Walk`, the layered ignore engine, `read_file`/`write_file`/`edit_file`, the two search backends, `find_files`, `list_files`, `file_outline`, `find_symbol`, `find_references` (spec 05), the extended subprocess runner (spec 04), `ReducedEnv`, spill. agent-fox imports 15 identifiers from here today and the `af` toolbelt will import more. | `fetch.go` and `ssrf.go` deleted: no coding pipeline fetches URLs, and the SSRF guard is 400 lines of attack surface guarding a tool nobody grants. `powershell.go` deleted: the "platform-stable tool list for the cache" argument only held when AgentKit owned every embedder's tool list. `proc_windows.go` stays so the runner cross-compiles. |
| `outline` | Repository map, structural checks, symbols and references all read it. | Unchanged. Issue 73's non-Go gaps remain its backlog. |
| `codesearch` (own module) | Opt-in indexed search. The separate module already isolates zoekt. | Unchanged and **frozen** until the navigation baseline in agent-fox is measured. ADR 07's go/no-go was skipped; no further work lands here until the number it was meant to rest on exists. |
| `guard` | The one program-side check a host hook can call. | Reshaped as a pure function, `guard.Check(argv []string, opts Options) Decision`, with the `BeforeToolCall` adapter kept as a one-liner over it. Issues 72 and 88 are fixed in that reshaping. Documented as best-effort: a floor, not a sandbox. |
| `schema` | agent-fox builds every submit-tool schema with its combinators. | `Validate` and `Coerce` deleted (issue 87: a half-implemented validator is worse than none). Argument validation moves to the wire: every tool is declared `strict: true`, so the API guarantees the arguments match the schema, and the handler decodes them. `jsonx` folds in as `schema.Parse([]byte)`, a 150-line order-preserving decoder, so a JSON Schema document becomes a `*Schema` without losing property order. |
| `core` | The vocabulary: messages, blocks, tools, results, events, usage. | Shrinks from 3 900 lines and 123 types to about 1 200 lines (§3). |
| `provider/anthropic` | The one wire. | Rewritten over `github.com/anthropics/anthropic-sdk-go`: encode the canonical request, decode the SDK's response and stream events. Vertex via `vertex.WithGoogleAuth`, Bedrock via the Mantle client. The hand-rolled transport, SSE decoder, retry ladder, credential store and OAuth refresh are deleted; the SDK owns them. |
| `provider/faux` | The scripted provider every test and agent-fox's whole suite runs against. | Unchanged in behaviour. |
| `catalog` | Model metadata the driver needs: context window, output cap, price, which thinking parameter the model takes. | Shrinks to a table of Claude rows and `catalog.Lookup(id)`. An unknown id is served with a warning and no price, not refused. Sibling cloning, tier aliases and clamping leave; agent-fox's tier table stays in agent-fox. |
| `prompt` | Base instructions plus per-tool guidelines, first-seen order, deduplicated. | Skill and project-context blocks deleted with the `skills` package. |
| root (`agentkit`) | The driver. | Rebuilt around what stays of the loop (§2). |

### Delete

| Package or file | Lines | Why it goes |
|---|---|---|
| `mcp` | 6 000 | Modern-only on a revision most servers have not adopted; its own README says it "cannot talk to a server that has not migrated, which today is most of them." The `af` toolbelt will be served over MCP from agent-fox with the official `modelcontextprotocol/go-sdk`. No embedder needs an MCP client inside the loop. Closes issue 86. |
| `provider/openai`, `provider/openairesponses`, `provider/google`, `provider/ollama`, `provider` (shared transport) | 9 100 | No user. The conformance suite, the three tool-result shapes, positional id synthesis and the cached-token netting table were real work solving a problem nobody here has. The ADR records them so they are not rediscovered. |
| `plugins`, `cmd/validate-plugins` | 1 200 | Four categories, a registry, a lint, and no plugin. |
| `skills`, `internal/toml`, `_skills/code-review`, `skillsconfig.go` | 3 100 | The only built-in skill is a Go review of this repository offered to every project (issue 91). agent-fox renders `AGENTS.md` into its own prompts and does not need a trust gate from the SDK. |
| `session`, `resume.go` | 2 700 | Nothing resumes a phase. agent-fox writes its own events file (its spec 12). The REQ-PROV-11 repair pass shrinks to the one rule a single-process run needs (§2). |
| `subagent` | 340 | Delegation is the embedder's loop calling the driver twice. |
| `compaction` | 720 | Summarization is a second model round trip with its own budget, its own failure modes (issues 74, 84) and a cache-invalidating edit. Replaced by `Prune` (§2), which is a hundred lines and no round trip. |
| `middleware`, `stop` | 900 | Retry is the SDK's `max_retries`. Budget, turns and duration become three config fields. Caching, tracing and rate limiting had no consumer. |
| `imagex`, `images.go` | 510 | No coding pipeline sends images. `read_file` on an image returns an error naming the format. |
| `wire` | 1 300 | The strict decoder existed for MCP's untrusted bytes. Provider responses are decoded by the SDK. |
| `jsonx` | 360 | Folded into `schema.Parse`. |
| `difftest` (own module) | 920 | Reports DARK by design and will stay dark: the reference bodies it needs cannot be produced without the thing it was meant to check. |
| `deferred.go`, `audit.go`, `execguard.go` (merged into the loop) | 300 | Deferred requests were never polled (issue 80). The hashed audit trail duplicates the event stream. |
| `examples/*` except `agentdemo`, `codingagent`, `customtools` | 13 000 | `cleaner`, `flatline` and `triage` are copies of agent-fox pipelines and drift from them. MCP, plugins, skills, session, delegation, interactive and streaming examples describe deleted features. |
| `internal/policy/deps_test.go` (stdlib rule) | | Replaced by an allowlist test (§4). The cross-target build gate stays. |
| `perf_budget_test.go` and every wall-clock threshold test | | They fail under load (issue 92, and two of them failed on a clean run while this PRD was written). What NFR-PERF-03 pins, the count of schema serializations, stays as a count. |

Roughly 40 000 lines of production code and 50 000 of tests leave the
repository. The module keeps about 12 000 production lines plus the
`codesearch` module's 3 500.

## 2. The driver

What survives of the loop is the part that is right and not in the SDK's
tool runner: the batch semantics, the events, the bounds, and ending a run
on a tool's say-so. The driver does not use `BetaToolRunner`, because those
four things are the reason it exists; it calls `Messages.New` and
`Messages.NewStreaming` directly.

```go
type Config struct {
    Client   *anthropic.Client   // built by anthropic.Resolve or the caller
    Model    string
    Effort   Effort              // low | medium | high | xhigh | max; maps to output_config.effort
    System   string
    Prefix   []core.Message      // sent after system on every request, with a cache breakpoint
    Tools    []core.Tool
    Guard    core.BeforeToolCall // required when any shell tool is in Tools (ErrUnguardedExecute)
    MaxTurns int
    MaxCostUSD float64
    Timeout  time.Duration
    Prune    PruneOptions        // zero value: off
    MaxTokens int
}

func New(cfg Config) (*Agent, error)
func (a *Agent) Run(ctx context.Context, prompt string) (core.RunResult, error)
func (a *Agent) Stream(ctx context.Context, prompt string) (*core.EventStream, error)
func (a *Agent) Messages() core.Messages
func (a *Agent) Usage() core.Usage
```

**Kept, with their tests:**

- Iteration on the presence of `tool_use` blocks, never on `stop_reason`.
- One `ToolResultMessage` per call; the wire coalesces.
- The batch executor: the abort decision made once before any handler
  starts, a result for every call, true concurrency without `errgroup`,
  the batch-scoped finalize mutex.
- `max_tokens` with tool calls executes none of them and answers each with
  the fixed synthetic result. This is the one repair rule that stays; the
  other six existed for resumed and damaged transcripts.
- `ToolResult.Terminate` ends the run after the batch; a rejected call does
  not. `RunStopToolTerminate`, `RunStopMaxTurns`, `RunStopBudgetExceeded`,
  `RunStopTimeout`, `RunStopAborted`, `RunStopRefusal`, `RunStopError`.
- Tool results reach the model as text; `Data` stays for interceptors.
- Tool-call argument bytes are replayed unchanged (`json.RawMessage` end
  to end), pinned by the golden request test.
- Every panic in an interceptor or handler is contained and reported.
- The event stream never blocks on its consumer.

**Added:**

- `Prefix` and its cache breakpoint. The SDK places `cache_control`; the
  driver stamps the last system block, the last tool, the last prefix
  block, and a rolling breakpoint on the last user message. This is PRD 06
  §3, which is the one part of that PRD agent-fox's token problem actually
  needs from the SDK.
- `Prune`: at a threshold fraction of the context window, tool results
  older than `KeepTurns` are replaced in the outbound view by one line
  naming the call and its size. Pairing with the `tool_use` block is kept.
  The transcript on `Messages()` is never edited; only the view sent is.
  This is PRD 06 §4 without read deduplication, which belongs in the tools
  if anywhere.
- `Effort` replaces `ThinkingLevel`. On every current Claude model the
  parameter is `output_config.effort` with adaptive thinking; `budget_tokens`
  is a 400. The catalog row says which models still take a budget, and
  the encoder uses it for those only.
- Every tool is declared `strict: true`. The handler decodes the arguments
  and does not validate them; a schema the API cannot enforce (a Go-side
  rule such as "cites a real path") is the handler's business and is
  returned as `core.ErrResult`, as today.
- **No forced tool choice.** Fable 5.1, Opus 5.5 and Sonnet 5.5 reject
  `tool_choice: any` and `tool_choice: tool` with a 400. PRD 06 §goal 6 ("a
  phase can require its terminating tool on a given turn") is withdrawn,
  not deferred: the mechanism does not exist on the models in use. A phase
  that ends without its terminator is a run that ends `RunStopEndTurn`
  with no result, and the embedder's prompt is the only lever.

**Dropped:** steering, follow-up, `Continue`, deferred requests and
redemption, the plugin and audit hooks, `SetModel` mid-run, the history
tree and branching, `ToolPolicy` (the config's `Tools` slice is the policy),
`ConstrainJSONSchema` and `StrictPrefer` (strict is unconditional), the
middleware chain, the summarizers.

## 3. `core` after the cut

| Stays | Goes |
|---|---|
| `Message`, `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `Messages`, `Role` | `ConversationHistory`, `SnapshotBranch`, the tree |
| `ContentBlock`: `TextBlock`, `ThinkingBlock`, `ToolUseBlock` | image and document blocks |
| `Tool`, `ToolResult`, `OKResult`, `ErrResult`, `BlockErrorCode`, `ToolMetadata` | `PrepareArguments`, `ToolPolicy`, `MCPServerOf` |
| `BeforeToolCall`, `BeforeToolCallContext`, `BeforeToolCallDecision`, `AfterToolCall`, `ErrUnguardedExecute` | `Plugin`, `EventHookPlugin`, `Tracer`, `AuditEvent` |
| `ProviderClient` (one method: `Stream(ctx, Request) (<-chan StreamEvent, error)`), `Request`, `StreamEvent` | `ProviderRegistry`, `ProviderStreamOptions`, `ClientFunc`, `Middleware` |
| `Event` and the run, turn, message, text, tool-call and tool-result events; `EventStream`; `MarshalEvent` | session-start and session-end audit events |
| `Usage` (input, output, cache read, cache write, requests, cost), `RunResult`, `RunStopReason`, `StopReason` | `ThinkingLevel` and its order (replaced by `Effort`) |
| `Model` (id, context window, max output, prices, thinking kind) | `Model.API`, `Model.Provider` |

Target: about 1 200 lines, under 40 exported types.

## 4. Rules that change

**The module path is the repository path.** `go.mod` becomes
`module github.com/agent-fox-dev/agentkit-go` and the proxy serves it.
agent-fox deletes its two `replace` directives and pins a tag. The
`codesearch` module follows.

**The standard-library rule is replaced by an allowlist.** The root module
may import `github.com/anthropics/anthropic-sdk-go` and what it requires,
and nothing else. `internal/policy` keeps the cross-target build gate for
`linux/amd64`, `linux/arm64`, `darwin/arm64` and `windows/amd64` and keeps
cgo forbidden. The allowlist test replaces the stdlib test and names the
file to edit when a module is added, with the one-sentence reason it must
carry in `go.mod` as a comment. `docs/DEPS.md` is deleted; the comment is
the ledger.

**The SDK is pinned to a minor version** and moved deliberately, with the
golden request test as the check. The request golden is the one artifact
in this repository that pins the wire against regression; it stays, for
one wire.

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
| `core.AgentConfig` (31 fields) | `agentkit.Config` (12 fields) |
| `core.Middleware`, `ClientFunc`, `middleware.Retry` | gone; `anthropic.Resolve` sets the SDK's retries and honours `Retry-After` |
| `core.ToolPolicy`, `ProviderRegistry`, `ProviderStreamOptions`, `ConversationHistory` | gone |
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

About two thirds of the identifiers agent-fox uses are untouched.
The rest are the loop's configuration surface, which agent-fox's
`agentrun` package wraps in one place and which the `af` work replaces.

## 6. What this cancels

- **PRD 03** (classic MCP client): withdrawn with the `mcp` package.
- **PRD 06**: §3 (stable prefix) and §4 (pruning) land here in §2; §5 and
  §7 (honest usage, guidelines under a custom prompt) become trivially
  true; §6 (the runner) shipped as spec 04 and stays; §8 (symbols) stays as
  PRD 04/07 work; §9 (stdlib) is reversed; §10 (every text a bundled
  document) and §12 are withdrawn, the driver has no prompt text beyond
  the base instructions and the guidelines, and agent-fox owns its own.
  Goal 6 (require the terminator) is withdrawn as impossible on current
  models.
- **`agent-kit-prd.md` 0.4.2** moves to `docs/archive/` as history. Its
  requirement ids stop being cited in new code and tests.
- **`docs/GAPS.md`, `docs/PROVIDERS.md`, `docs/DEPS.md`, `docs/api.md`,
  `docs/cli.md`** are deleted. The errata that describe deleted packages
  go with them; the rest are re-read and kept only where the code they
  describe survives.
- Open issues 86 and 91 close by deletion; 92 closes with the README
  rewrite.

## 7. Order of work

Each step is one pull request and leaves `go test ./...` green in this
repository. agent-fox stays on the frozen tag until step 6 and is not
rebuilt against intermediate steps.

1. **Freeze.** Tag `main` as `v0.4.2`. agent-fox pins it by `replace` to
   that checkout, which it already does in effect.
2. **The cut.** Delete the packages, files and examples in §1. Rename the
   module. Replace the stdlib test with the allowlist test. Delete the
   wall-clock tests. The root tests that drove deleted features go with
   them; the ones that pin kept behaviour are kept and must still pass.
   This is the largest diff and the most mechanical; it ships first so
   every later step is reviewed against a small tree.
3. **The wire.** Rewrite `provider/anthropic` over the SDK. `anthropic.Resolve`.
   `catalog.Lookup`. `Effort`. Strict tools. The golden request test is
   rewritten for the new encoder and is the acceptance test.
4. **The driver.** `agentkit.Config` and `New`. Fold bounds into config.
   Delete steering, `Continue`, deferred. Add `Prefix` stamping and
   `Prune`. The scripted-provider tests in §8 are the acceptance test.
5. **`core`, `schema`, `guard`.** The §3 shrink, `schema.Parse`,
   `guard.Check`.
6. **Docs and tag.** README, `architecture.md`, `configuration.md`
   rewritten; `agent-kit-prd.md` archived; the rest deleted. Tag `v0.5.0`.
   agent-fox moves to the tag, drops `replace`, deletes its non-Claude
   provider tests and its `compaction`/`stop`/`middleware` wiring, in its
   own PR under its own PRD.

Steps 3, 4 and 5 are independent of each other once step 2 has landed and
may be worked in parallel.

## 8. Acceptance criteria

- `git ls-files '*.go' | grep -v _test | xargs wc -l` over the root module
  reports under 15 000 lines; under 25 000 for tests.
- `go list -m all` in the root module lists `anthropic-sdk-go` and its
  requirements and no other third-party module; the allowlist test fails
  on any addition not named in it.
- No directory named `mcp`, `plugins`, `skills`, `session`, `subagent`,
  `difftest`, `imagex`, `compaction`, `middleware`, `stop`, `wire`, `jsonx`
  or `internal/toml` exists; `tools/fetch.go`, `tools/ssrf.go` and
  `tools/powershell.go` do not exist.
- `go get github.com/agent-fox-dev/agentkit-go@v0.5.0` resolves through the
  public proxy; agent-fox at its matching commit builds with no `replace`
  directive in `go.mod`.
- `grep -rn 'time.Since\|Elapsed' --include='*_test.go'` finds no test that
  compares a duration against a threshold to decide pass or fail.
- The following run against `provider/faux` through `agentkit.New` and
  are named in the ADR: a three-call batch runs its handlers concurrently
  and returns three results; a cancellation mid-batch runs zero or three
  handlers, never one; a `max_tokens` turn with a tool call executes
  nothing and answers it with the fixed text; a `Terminate` result ends
  the run after its batch while a rejected call does not; `Prune` elides
  a result older than `KeepTurns` from the outbound view and leaves
  `Messages()` intact; `MaxCostUSD` stops the run with
  `RunStopBudgetExceeded` before the request that would exceed it; a
  shell tool with a nil `Guard` is refused by `New`.
- The golden Anthropic request shows: `cache_control` on the last system
  block, the last tool, the last prefix block and the last user block;
  `strict: true` on every tool; `thinking: {type: adaptive}` and
  `output_config.effort` for a current model and no `budget_tokens`; a
  replayed `tool_use` input byte-identical to the one decoded.
- `anthropic.Resolve` returns a Vertex-configured client when
  `CLAUDE_CODE_USE_VERTEX` is set and a Bedrock one when
  `CLAUDE_CODE_USE_BEDROCK` is set, pinned by a test that inspects the
  client's base URL and auth source without a network.
- `README.md` is under 200 lines and names no test function.
- Issues 86, 91 and 92 are closed.

## 9. What is given up, stated plainly

- **Other vendors.** An embedder on OpenAI, Gemini or Ollama writes a
  provider. The interface is one method and the faux provider is the
  template. The conformance suite that made four wires agree is deleted
  with them, and the ADR keeps its findings so the traps are not
  rediscovered.
- **The MCP server and client.** A server for the `af` tools lives in
  agent-fox on the official SDK. Nothing in the driver can call an MCP
  tool. If that is ever needed, the SDK's own MCP connector (`mcp_servers`
  on the request) is the route, not a client in this module.
- **Resume.** A run that dies is re-run. agent-fox already treats every
  phase as fresh and parks work in git, which is the durable state.
- **Summarization.** A transcript that outgrows the window after pruning
  ends the run with `RunStopError` naming the window. Pruning at 35% of
  the window and the 1M-token context of current models make this a
  configuration error, not an operating condition.
- **Control over the wire bytes.** The SDK marshals the request. Cache
  stability is verified by the golden test and by `Usage.CacheRead`, not
  by owning the encoder. A silent invalidator introduced by an SDK update
  is caught by the golden, which is why the SDK is pinned.
- **The stdlib purity.** It bought a cgo-free cross build, which the gate
  keeps, and nothing else anyone asked for.

## Documentation

- `README.md`: what the two things are, the ten-line driver example, the
  consumer contract table from §5 reduced to the kept identifiers.
- `docs/architecture.md`: the package graph of §1 and the import rule
  (everything imports `core`; `tools` imports `outline`; the root imports
  `tools`, `prompt`, `catalog` and `provider/anthropic`; nothing imports
  the root).
- `docs/configuration.md`: `Config`, `anthropic.Resolve` and the three
  environment variables it reads, `PruneOptions`, the catalog table.
- `docs/adr/02-cut-agentkit-down-to-the-hands.md`: the decision, the
  deleted packages with the findings worth keeping from each, and the
  named tests of §8.
- `docs/archive/agent-kit-prd.md`: the 0.4.2 PRD, unchanged, with a
  one-line header pointing here.
