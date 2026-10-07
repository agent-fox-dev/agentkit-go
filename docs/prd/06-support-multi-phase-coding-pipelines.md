# Support multi-phase coding pipelines

Status: **proposed**. Written for agent-fox
[PRD 13](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prds/13-rebuild-fix-on-a-shared-change-engine.md)
and [PRD 14](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prds/14-rebuild-impl-on-the-shared-change-engine.md),
which rebuild `fix` and `impl` on a shared engine and need nine things from
the SDK they build on. The split follows agent-fox
[ADR 07](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/adr/07-split-code-navigation-between-agentkit-and-agent-fox.md):
mechanism here, policy and prompts there. Nothing in this PRD names a phase,
a spec or an envelope. Amended on 2026-10-07 with §12, prompt text as
documents.

## 1. The problem

An embedder that runs a long job as a sequence of agents — agent-fox's
`impl` runs a survey, one agent per task, a review, a resolve, each a fresh
`Agent` with its own transcript — meets the SDK at nine edges where it
either re-implements a mechanism the SDK already has in a less safe form, or
pays tokens the SDK could have saved.

The numbers come from one accounted `impl` run (agent-fox #194 to #200): 420
turns, every one carrying about 100 000 cached tokens, $15.38; nine agents
built in a row with identical tools and system prompt, none sharing a cache
prefix with the one before, because the tool descriptions carried a random
directory name; a transcript that grew with every `read_file` and `execute`
result until summarization fired, whose own request of several hundred
thousand tokens was billed to nobody; a `max_tokens` turn that ended a phase
with no result because nothing could force the terminating tool; a shell
guard the embedder re-segments because the SDK's judges only the first
program; a process runner the embedder re-wrote without process-group kill.

## 2. Goals and non-goals

**Goals.**

1. Two agents built in a row from the same tools, system prompt and a
   constant first message share a provider cache prefix, and the embedder
   can see that they did.
2. A transcript can be pruned — old tool results elided, repeated reads
   answered by reference — without summarization, and transforms compose.
3. Every token the SDK spends on an agent's behalf is in `Agent.Usage`,
   summarization included, and an aborted summary never becomes a
   checkpoint.
4. An embedder can run a subprocess with the SDK's process control and get
   a result that tells a timeout from a cancellation from a failure.
5. A custom system prompt keeps the tool guidelines.
6. A phase can require its terminating tool on a given turn.
7. Tool results carry their metadata to the embedder.
8. Symbol navigation is as good for a Java, TypeScript, Ruby or Rust method
   as for a Go one, with or without ctags, and an embedder's outline agrees
   with `file_outline`.
9. The root module stays standard-library-only and cgo-free (REQ-GO-11).
10. Every text the SDK puts in front of a model — base instructions,
    guidelines, tool descriptions, refusals, summarizer prompts, synthetic
    results — is a document bundled with the binary, listed with its hash,
    and replaceable by name, so an embedder can audit and override what
    its agents read without patching strings.

**Non-goals.**

- A pipeline, workflow or phase abstraction. The embedder sequences agents;
  the SDK makes each cheaper and safer.
- A language server, or type resolution for languages other than Go.
- Server-side compaction, batch pricing or deferred requests as a
  requirement of any item here; where they exist they stay optional.
- Changing the loop's iteration rule, the batch executor or the wire
  encoders beyond what §1 and §7 need.

## 3. The stable prefix (goal 1)

Today `StampCacheControl` (`provider/anthropic/anthropic.go`) places three
breakpoints: the last system block, the last immediate tool, and a rolling
one on the last block of the last user message. An embedder that starts a
new agent with the same tools and system prompt and the same first user
message gets a prefix hit only if that message's breakpoint entry is still
alive from the previous agent's first turn, and never for a message the
rolling breakpoint has moved past.

```go
type AgentConfig struct {
    // PrefixMessages are sent after the system prompt and before the
    // conversation on every request, byte for byte, and end in a cache
    // breakpoint of their own. They are not part of History, are never
    // compacted or pruned, and a session log records them once.
    PrefixMessages []core.Message
    // CacheRetention applies to the tools, the system prompt and the
    // prefix; the conversation keeps the rolling breakpoint.
    CacheRetention core.CacheRetention
}
```

- The Anthropic encoder stamps a fourth breakpoint on the last block of the
  last prefix message, with the configured retention (`long` puts `ttl:1h`
  on all four). The OpenAI wires send `prompt_cache_key` from `SessionID`
  as today and the prefix as leading messages; Gemini maps the prefix to
  `CachedContent` where the account allows and sends it inline otherwise.
- `provider.ToolPrefix` is built per agent (in `newAgent`, carried in
  `ProviderStreamOptions`) rather than per provider value, so
  `CacheStats.PrefixInvalidations` is meaningful across agents and an
  embedder with one provider per run no longer reads "tool removed" at
  every agent boundary.
- `CacheStats` gains `PrefixHitFirstTurn bool` and
  `PrefixTokens int`: whether the first request of the agent read its
  tools+system+prefix from cache, and how many tokens that was. This is
  the number agent-fox reports as `cached_prefix_tokens`.
- A test builds two agents in a row against a recording provider and
  asserts the second's first request is byte-identical to the first's up to
  the prefix breakpoint, and that `PrefixHitFirstTurn` is reported from the
  provider's `cache_read_input_tokens`.

## 4. Pruning and composition (goal 2)

`compaction.NewContextTransform` is the only `TransformContext` an agent can
have, and summarization is the only strategy. Two additions:

```go
// Prune elides tool results that are older than KeepTurns turns or that
// push the transcript over MaxBytes, oldest first, replacing each body with
// one line: "[result of read_file internal/x.go (412 lines, 18 KB) elided
// at turn 23; call again if needed]". ToolUseID pairing is kept, so
// RepairTranscript never sees an orphan. A result whose message the
// embedder marked Pinned is never elided.
func Prune(opts PruneOptions) core.ContextTransform

// Chain applies transforms in order on every request.
func ChainTransforms(ts ...core.ContextTransform) core.ContextTransform
```

- Pruning fires in batches at a threshold (`PruneOptions.Threshold`, a
  fraction of the window, default 0.35), never per turn: every edit moves
  the cache divergence point back to the edited message, so a prune is a
  deliberate cache rewrite of the tail, taken when the tail is worth
  rewriting. Summarization at its own threshold stays the backstop.
- `PruneOptions.OnPrune func(elided int, bytes int)` reports what was
  removed; agent-fox puts it in `usage.phases[].pruned_tokens`.
- **Read deduplication.** `tools.Options.DedupeReads bool`: the file tools
  remember `(path, offset, limit) → (content hash, turn)` for `read_file`,
  and `(arguments) → (hash, turn)` for `search_files`, `find_files`,
  `file_outline`, `find_symbol` and `code_search`. A repeated call whose
  answer is unchanged returns one line — `[unchanged since turn N; the
  result is above]` — unless that earlier result has been pruned, in which
  case the full result is returned again. The turn index reaches the tool
  through `BeforeToolCallContext.TurnCount`, already on the context; the
  pruner tells the dedupe table which turns it elided through the same
  table. A write through `write_file`, `edit_file` or a shell tool
  invalidates the table's entries for the paths it touched (§10).

## 5. Honest usage (goal 3)

- `compaction.Summarizer` and `TurnSummarizer` return `(text string, usage
  core.Usage, err error)`. `NewContextTransform` folds the usage into the
  agent through `Deps.OnUsage`, which `NewAgentWithHistory` wires to
  `addUsage`, so `Agent.Usage`, `stop.OverBudget`, `middleware.Budget` and
  `CacheStats` see it.
- `ValidateSummary` rejects `StopReasonAborted` as it rejects
  `StopReasonLength`: an aborted summary is not a checkpoint. The
  transform returns the uncompacted view and reports through `OnError`.
- The summarizer's request goes through the agent's middleware chain, so
  `Retry` covers it and a transient failure does not re-send the whole
  prefix unbilled.
- `docs/configuration.md` states what `Agent.Usage` includes.

## 6. A process runner for embedders (goal 4)

`tools.Run` has the process control an embedder needs — a process group, a
kill on cancellation, a re-arming drain, the reduced environment — tied to
tool semantics (spill files, tail truncation, a status line). Export the
control without the semantics:

```go
type ExecResult struct {
    Output   string // bounded by MaxBytes, head or tail per KeepTail
    ExitCode int    // -1 when the process did not run to an exit
    TimedOut bool   // the deadline opts.Timeout set expired
    Aborted  bool   // ctx was cancelled before exit
    Duration time.Duration
}

func Exec(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error)
```

`ExecOptions` carries `Dir`, `Env` (nil means `ReducedEnv`), `Stdin`,
`Timeout`, `WaitDelay`, `MaxBytes`, `KeepTail` and `LogPath` (the full
output written to a file as it arrives, so a caller can keep a log the model
reads on demand). `TimedOut` and `Aborted` are never both true, and
`Aborted` is set from the caller's context, not from the derived deadline:
the distinction agent-fox's verifier could not make (agent-fox #216). The
tool's own `Run` is rewritten over `Exec`.

## 7. Guidelines under a custom prompt (goal 5)

`prompt.Build` replaces the base instructions **and** the guidelines block
when `Custom` is set. `Input` gains `KeepGuidelines bool`: the guidelines
block, including the conditional `ExecuteFallbackGuideline` and
`SearchOverExecuteGuideline`, is rendered after the custom text, in tool
order, blanks and repeats removed, exactly as it is rendered under the base
prompt. `guidelinesBlock` keys the shell guidelines on the registered shell
tool's name, not on the literal `execute`. An embedder's own rendering
(agent-fox's `toolsNote`) is deleted. `docs/configuration.md`'s
`SystemPrompt` row says what a custom prompt drops and keeps.

## 8. A required tool (goal 6)

`core.ToolChoice` gains `ToolChoiceRequired` and `ToolChoiceTool(name)`,
encoded on every wire that has them (Anthropic `{type: "tool"}` and
`{type: "any"}`, OpenAI `required` and `{type: "function"}`, Gemini
`ANY` with `allowedFunctionNames`) and reported as unsupported through
`Model.Capabilities` where a wire has none, so an embedder can decide. A
`StopPolicy` result gains `Choice core.ToolChoice`, so a policy can say
"one more turn, with this tool": agent-fox's phase runner forces the
terminator on the last allowed turn instead of ending in `NoResultError`.
The repair loop is unchanged: a rejected call is still a tool error the
model answers.

## 9. Tool metadata on the result (goal 7)

`core.ToolResultMessage` gains `Metadata *ToolMetadata` (populated by the
batch executor from the tool's `ToolResult`, persisted in the session log as
a sibling field), and `AfterToolCallContext` carries the `ToolResult`
itself. An embedder reads `ExitCode`, `Truncated` and `SpillPath` from the
message instead of parsing `[exit N]` out of the text. The Anthropic,
OpenAI, Gemini and Ollama encoders ignore the field.

## 10. Symbol navigation for every language (goal 8)

The findings of agentkit-go #73 become requirements:

- A ctags tag of kind `method` (or `function`) whose scope kind is
  type-like — `class`, `struct`, `interface`, `enum`, `trait`, `impl`,
  `implementation`, `type`, `module` where the language uses it for a type
  — gets `Container = scope`. `find_symbol Runner.Run` and
  `code_search sym:Class.method` then work for Java, Kotlin, C#, TypeScript,
  JavaScript, Ruby, PHP, Swift, Scala and Rust.
- A file for which ctags emits no tag falls through to the heuristic
  backend; `Backend` says which one answered.
- The extension table no longer gates ctags: a file with an extension the
  table does not know is handed to ctags when ctags is present, and only
  `none` when it is not.
- The heuristic backend gains an **indented-member pass**: a declaration
  rule that fires at the first indentation level under a `class`, `impl`,
  `trait`, `object`, `module` or `namespace` opener produces a `method`
  with that container, for Python, Java, Kotlin, C#, Ruby, TypeScript,
  JavaScript and Rust; and the mainstream forms #73 lists (`export abstract
  class`, `export const f = () =>`, `pub async fn`, `public abstract class`,
  `def self.x`, C# block namespaces, Kotlin modifiers, `enum class`) are
  found. Fixtures under `outline/testdata/` cover each.
- `EndLine` is filled from ctags' `end` field wherever ctags gives it, so
  an embedder can measure a declaration's length in any language ctags
  parses.
- `tools.OutlineRunner(opts)` is exported, so an embedder's outline uses
  the same ctags runner and batching (`OutlineMany`) the tools use, and
  agrees with `file_outline` file for file.
- `outline.Classify(path) FileClass{Test, Generated, Vendored}` is one
  table of file conventions per language, used by `find_symbol`'s ranking
  and `code_search`, and exported so an embedder's test-file rule is the
  same table rather than a third copy. It is a convention table, not a
  policy: what an embedder does with a test file is its own.

## 11. A workspace change journal (goal 2, goal 8)

The symbol table, the code-search index and §4's dedupe table each keep a
dirty set, and a shell call marks everything dirty in each. One journal
replaces them:

```go
func (ws *Workspace) Journal() *ChangeJournal   // Mark(rel), Since(gen) []Change, Gen() uint64
```

`write_file` and `edit_file` mark the path they wrote; a shell tool marks
the paths an `(size, mtime)` scan of the walked tree finds changed, bounded
by the symbol table's limits, and marks everything only when the scan is
cut short. The table, the index and the dedupe cache ask `Since(gen)` and
revalidate what changed. An embedder that changes the tree itself (a
checkout, a reset) calls `Mark` or `MarkAll`, which is what agent-fox's
invalidation does today through two separate calls.

## 12. Prompt text as documents (goal 10)

`prompt.BaseInstructions`, `UniversalGuidelines`, every built-in tool's
`Description` and `PromptGuidelines`, `ExecuteFallbackGuideline` and
`SearchOverExecuteGuideline`, `guard.Restricted`'s refusal sentences, the
compaction summarizer and turn-summarizer system prompts, and
`provider.SyntheticResultText` are Go string constants spread over six
packages. An embedder that wants to know, or change, what its agents read
greps for them, and one that wants a phase-specific tool description
appends to the SDK's string (agent-fox's `describeForPhase`). They become
documents:

- **One tree.** `prompt/texts/` holds every text as a Markdown file with a
  small YAML header (`name`, `kind`: `instructions | guideline | tool |
  refusal | summarizer | synthetic`, `for`: the tool or package it belongs
  to), embedded with `go:embed`. A package that owns a text (`tools`,
  `guard`, `compaction`, `provider`) reads it through
  `prompt.Text(name) string`, which panics at init on a missing name, so a
  renamed file is a build failure, not a hole in a prompt. Package
  dependencies are unchanged: `prompt` imports only the standard library
  and `core`, as ADR 01 requires, and the texts are data.
- **Templates where a text has holes.** A text that carries a value — a
  tool description that names its limits (`read_file`'s 2 000 lines), a
  refusal that names the programs refused, the summarizer prompt that
  names the reserve — is a `text/template` over a small typed value with
  `missingkey=error`, rendered by `prompt.Render(name, view)`. Values are
  inserted verbatim and never parsed, so a refusal that quotes a command
  containing `{{` is text. Texts without holes are plain Markdown.
- **A catalog.** `prompt.Catalog() []TextInfo{Name, Kind, For, SHA256}`
  lists every embedded text with the hash of its source, so an embedder can
  record which text version an agent ran with; agent-fox writes it beside
  its own `prompt_templates`.
- **Replacement by name.** `AgentConfig.Texts map[string]string` and
  `tools.Options.Texts` let an embedder replace any text by name for one
  agent or one tool set: a whole description, a whole guideline, a whole
  refusal. The SDK renders the replacement with the same view the original
  gets, so an embedder's `execute` description can name the SDK's limits
  without copying them. A replaced text is reported in `Catalog()` with
  `Replaced: true` and the replacement's hash. This is what lets agent-fox
  make a tool description one document instead of a base string plus
  patches (agent-fox PRD 13 §6.6).
- **Golden renderings.** `prompt/testdata/golden/` already holds the
  assembled prompt's goldens; every text and every templated text gains
  one, and `UPDATE_GOLDEN=1` regenerates them, so a change to what a model
  reads is a Markdown diff in review.
- Nothing is sent that is not in the catalog: a test walks the SDK's
  packages for string literals longer than one sentence outside
  `prompt/texts/` and the tests, and fails on any that reaches a provider
  request.

## 13. Tests, documentation, order

- Every item has a test in its package; §3 and §4 also have a test against
  the recording provider that asserts request bytes.
- `internal/policy` stays green: no new module, no cgo, all cross-targets.
- `docs/configuration.md` (`PrefixMessages`, `CacheRetention`, `Prune`,
  `DedupeReads`, `KeepGuidelines`, `ToolChoice`, `Texts`), `docs/api.md`
  (`Exec`, `OutlineRunner`, `Classify`, `Journal`, `prompt.Text`,
  `Render`, `Catalog`), `docs/architecture.md` (the journal in the data
  flow; the texts tree), README (what is not built: callers stay; "methods
  without containers" leaves), `docs/GAPS.md`.
- Order, by what agent-fox needs first: §6 and §9 (the engine's runner and
  metadata), §7, §5 and §12 (correctness and the documents, small), §3 and
  §4 (the token goal), §10 and §11 (language equality), §8 last.
