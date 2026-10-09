---
spec_id: "12"
spec_name: "core_schema_guard"
title: "Shrink Core Vocabulary, Fold jsonx into schema.Parse, Reshape guard.Check, and Rewrite Docs"
status: "active"
created_at: "2026-10-09T11:53:55.402368Z"
updated_at: "2026-10-09T11:53:55.402368Z"
intent_hash: "f6d492819c1bf686f2569744627232a5767737b072ef57868ab25336161aa6ea"
schema_version: 2
source: "docs/prd/10-cut-agentkit-down-to-the-hands.md"
---
## Intent

Streamline the core package vocabulary to under 50 exported types and 1,600 production lines, retire `jsonx` by folding order-preserving schema decoding into `schema.Parse` with complete constraint validation, reshape `guard.Check` as a pure execution authorization function, and rewrite project documentation to reflect the trimmed architecture.

## Goals

1. Shrink `core` to under 50 exported types and under 1,600 lines of production Go, deleting session tree types (`ConversationHistory`, `SnapshotBranch`), legacy `AgentConfig` and hooks, `ImageBlock`, `MCPServerOf`, `ThinkingLevel`, and multi-provider registry abstractions.
2. Define `core.ProviderClient` with a single streaming signature (`Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)`) and define canonical `StreamEvent` and `Request` types without provider registry or middleware couplings.
3. Delete the `jsonx` package and directory entirely, eliminating `jsonx` imports across `core`, `schema`, `tools`, and `mcp`.
4. Implement `schema.Parse([]byte) (*Schema, error)` using `encoding/json/jsontext` to construct `*Schema` values that preserve property order in `PropertyOrder []string`.
5. Complete JSON Schema constraint validation and type coercion in `schema` addressing issue 87 gaps: strict `additionalProperties: false`, `enum` verification, numeric bounds (`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`), string length and patterns, and array item cardinality.
6. Reshape `guard.Check(argv []string, opts Options) Decision` as a pure function returning a concrete `Decision` struct (`Block`, `Reason`, `Terminate`), with `Restricted` providing a one-line `core.BeforeToolCall` adapter over it.
7. Simplify `prompt.Build(system string, tools []core.Tool) string`, deduplicating per-tool guidelines in first-seen order and omitting deleted `skills` dependencies.
8. Rewrite `README.md` under 200 lines without citing test function names, documenting "the hands" and one driver, quickstart, code mode, and consumer contracts (closing issues 91 and 92).
9. Rewrite `docs/architecture.md`, `docs/configuration.md`, and `docs/DEPS.md`, archive `agent-kit-prd.md` to `docs/archive/agent-kit-prd.md` with historical multi-wire findings in `docs/archive/wires.md`, and delete `docs/api.md` and `docs/cli.md`.
10. Ensure `make check` succeeds cleanly and total production lines across the root module remain under 20,000 lines.

## Non-goals

- Backwards compatibility with 0.4.x APIs for deleted `core` types or `jsonx`.
- Rewriting `provider/anthropic` over the official SDK or model catalog resolution (delivered in `10_anthropic_wire`).
- Re-architecting the agent driver loop, prefix caching breakpoints, or transcript pruning (delivered in `11_minimal_driver`).
- Removing provider backends, obsolete tools, or wall-clock threshold tests (delivered in `09_repository_cut`).
- Retaining interactive agent features (steering queues, resumption from disk, multi-turn history branching).
- Implementing an MCP server in this repository (the `af` tools server belongs in `agent-fox`).

## Background

AgentKit was initially specified as an expansive, dependency-free agent SDK supporting five provider wire APIs, bidirectional MCP, plugins, skills, sessions, compaction, delegation, and differential testing. In that design, `core` grew to over 3,300 lines of production code and 124 exported types across `core/history.go`, `core/config.go`, `core/provider.go`, `core/message.go`, and related files. It held complex session logs, branching trees, audit event sink abstractions, and multi-provider registry tables.

In practice, AgentKit's sole consumer (`agent-fox`) operates exclusively on Claude models and uses only AgentKit's workspace mechanics ("the hands") and a minimal driver loop. Earlier scopes removed dead provider backends and auxiliary subsystems (`09_repository_cut`), rebuilt the Anthropic wire over the official SDK (`10_anthropic_wire`), and introduced the minimal driver loop in `agentkit.Config` (`11_minimal_driver`).

What remains is to complete the foundational cleanups:
1. `core` still holds dead types: `ConversationHistory`, `SnapshotBranch`, `AgentConfig`, `Hooks`, `ImageBlock`, `MCPServerOf`, and `ThinkingLevel`.
2. `jsonx` is a 360-line standalone package for ordered JSON parsing; its sole ongoing consumer is JSON Schema property ordering, which can be cleanly folded into `schema.Parse` using Go's `encoding/json/jsontext`.
3. Issue 87 identified validator gaps in `schema` (`additionalProperties: false`, `enum`, bounds) that matter acutely now that model-written Starlark scripts (`codemode`) validate nested arguments against tool schemas directly in-process.
4. `guard` couples authorization checks to `core.BeforeToolCallContext` instead of exposing a pure function `guard.Check(argv []string, opts Options) Decision` that can be tested and reused outside hook contexts.
5. Project documentation (`README.md`, `docs/architecture.md`, `docs/configuration.md`, `docs/DEPS.md`) is out of date, describing deleted packages (`skills`, `plugins`, `compaction`) and citing fragile wall-clock tests (issues 91, 92).

This specification finalizes the vocabulary shrink, integrates ordered schema parsing, makes the shell guard a pure function, and updates all project documentation.

## Requirements

### Core Vocabulary Shrink

1. In `core/message.go`:
   - Keep: `Message`, `UserMessage`, `AssistantMessage`, `ToolResultMessage`, `Messages`, `Role`.
   - In `UserMessage`, `AssistantMessage`, and `ToolResultMessage`, remove `Unknown jsonx.OrderedObject` and `Deferred *DeferredHandle`.
   - Content blocks shall be restricted to `TextBlock`, `ThinkingBlock`, and `ToolUseBlock`. `ImageBlock`, `RawBlock`, and `ToolResultBlock` shall be removed.
   - `ToolUseBlock` shall keep `ID`, `Name`, `Input json.RawMessage`, and `ThoughtSignature string`. The field `InputOrder jsonx.OrderedObject` shall be removed.
   - Assistant messages shall replace `ThinkingLevel` with `Effort` (or catalog-based thinking descriptors) and remove legacy provenance fields (`API`, `Provider`).
2. In `core/arguments.go`:
   - `PreparedArguments` shall contain `Raw json.RawMessage`, `Args map[string]any`, and `Coercions []schema.Coercion`.
   - All references to `jsonx.OrderedObject` in `PreparedArguments` and `PrepareArguments` shall be removed. Argument byte stability shall be preserved by retaining the original `Raw` bytes unless coerced or repaired.
3. In `core/provider.go`:
   - `ProviderClient` shall be defined as an interface with exactly one method:
     ```go
     type ProviderClient interface {
         Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
     }
     ```
   - `StreamEvent` shall be defined as a container carrying stream progress: an event `Event` and an optional terminal error `Err error`.
   - `Request` shall contain:
     ```go
     type Request struct {
         System        []ContentBlock
         Messages      Messages
         Tools         []ToolWire
         ToolChoice    ToolChoice
         MaxTokens     *int
         Temperature   *float64
         TopP          *float64
         StopSequences []string
         Effort        Effort
     }
     ```
   - The following obsolete provider types shall be deleted: `ProviderRegistry`, `ProviderStreamOptions`, `ClientFunc`, `StreamFunc`, `APIProvider`, `Complete`, and `Dispatch`.
4. In `core/tool.go`:
   - `Tool` shall retain `Name`, `Description`, `InputSchema`, `OutputSchema`, `ReachableTools`, `Terminating`, `ExecutionMode`, `PromptGuidelines`, `Handler`, `Execute`, and `PrepareArguments`.
   - `ToolWire` shall omit `ConstrainedSampling`.
   - `MCPServerOf` shall be removed.
5. In `core/usage.go`:
   - Keep `Usage` (with `InputTokens`, `OutputTokens`, `CacheReadTokens`, `CacheWriteTokens`, `Requests`, `CostUSD`), `RunResult`, `RunStopReason`, and `StopReason`.
   - Remove `ThinkingLevel` enum and its ordering predicates.
   - `Model` shall retain `ID`, `ContextWindow`, `MaxOutputTokens`, `InputCostPerMillion`, `OutputCostPerMillion`, `CacheReadCostPerMillion`, `CacheWriteCostPerMillion`, and `ThinkingKind`. Remove `Model.API` and `Model.Provider`.
6. Files deleted from `core`:
   - `core/history.go` shall be deleted completely (`ConversationHistory`, `SnapshotBranch`, `SessionHeader`, `Entry`, `EntryID`, `MessageEntry`, `CompactionEntry`).
   - `core/config.go` shall be deleted completely (`AgentConfig`, `StopContext`, `StopPolicy`, `ContextTransform`, `Hooks`, `QueueMode`).
7. Package size: `core` shall not exceed 50 exported types and shall contain approximately 1,600 lines of production code.

### Retirement of jsonx and Schema Parsing

8. The `jsonx` directory and all files within it (`jsonx/ordered.go`, `jsonx/ordered_probe_test.go`) shall be deleted from the repository.
9. All imports of `github.com/agent-fox-dev/agentkit-go/jsonx` (or `agentfox/agentkit-go/jsonx`) across `core`, `schema`, `tools`, and `mcp` shall be removed.
10. `schema` shall provide `Parse(data []byte) (*Schema, error)`:
    - `Parse` shall decode JSON Schema documents using `encoding/json/jsontext`.
    - It shall preserve the authored property declaration order in `PropertyOrder []string`.
    - It shall parse schema properties into `Properties map[string]*Schema`.
    - It shall parse `required`, `type`, `description`, `title`, `items`, `enum`, `const`, `nullable`, `anyOf`, `oneOf`, and `allOf`.
    - It shall preserve unknown or unmodeled keywords in `Extra` without losing key ordering.
11. Issue 87 validator constraints in `schema.Validate`:
    - When `AdditionalProperties` is configured with `Allowed: false`, `Validate` shall reject any object property not declared in `Properties` or `PropertyOrder`.
    - When `Enum` values are defined, `Validate` shall verify that the candidate value matches one of the declared JSON literals byte-for-byte or value-for-value.
    - When numeric bounds are defined (`Minimum`, `Maximum`, `ExclusiveMinimum`, `ExclusiveMaximum`, `MultipleOf`), `Validate` shall enforce the respective relational checks.
    - When string bounds are defined (`MinLength`, `MaxLength`, `Pattern`), `Validate` shall enforce length and regular expression matches.
    - When array bounds are defined (`MinItems`, `MaxItems`, `UniqueItems`), `Validate` shall enforce element counts and uniqueness.
12. `schema.Coerce` shall safely parse primitive string values into numeric or boolean representations without emitting invalid JSON literals (rejecting `"NaN"`, `"Inf"`, `"+5"`, `".5"`).

### Pure-Function Shell Guard

13. `guard` shall define `Decision`:
    ```go
    type Decision struct {
        Block     bool
        Reason    string
        Terminate bool
    }
    ```
14. `guard` shall expose `Check(argv []string, opts Options) Decision` as a pure function:
    - If `len(argv) == 0`, `Check` shall return `Decision{Block: true, Reason: "empty argv", Terminate: opts.TerminateOnBlock}`.
    - `Check` shall evaluate `argv[0]` against `opts.AllowedPrograms`:
      - If `argv[0]` contains a path separator (`/` or `\`), it shall match only exact cleaned path entries in `AllowedPrograms`.
      - If `argv[0]` is a bare name, it shall match bare program names in `AllowedPrograms`.
      - If `argv[0]` is not permitted, `Check` shall return `Decision{Block: true, Reason: fmt.Sprintf("program %q is not on the allowlist", argv[0]), Terminate: opts.TerminateOnBlock}`.
    - If permitted, `Check` shall return `Decision{Block: false}`.
15. In `guard/guard.go`, `ShellToolNames` shall be updated to `[]string{"execute", "run_command"}` (omitting deleted `powershell`).
16. `Restricted(opts Options) core.BeforeToolCall` shall be a wrapper over `guard.Check`:
    - If the tool is in `opts.BlockedTools`, it shall block with the refusal reason.
    - For `run_command`, it shall convert `arguments["argv"]` to `[]string` and delegate to `guard.Check(argv, opts)`.
    - For `execute`:
      - If `opts.AllowShellOperators` is false and shell operators are present, it shall block.
      - If environment assignment prefixes exist and `opts.AllowEnvPrefixes` is false or targets dangerous variables (`PATH`, `LD_*`, `BASH_ENV`), it shall block.
      - It shall parse the program word and delegate authorization to `guard.Check([]string{prog}, opts)`.
    - If `guard.Check` blocks, `Restricted` shall return `core.BeforeToolCallDecision{Block: true, Reason: "guard.Restricted: " + decision.Reason, Terminate: decision.Terminate}`.
17. The documentation for `guard` shall state clearly that `Restricted` is a floor for unattended safety, not a sandbox.

### Prompt Assembly Simplification

18. `prompt.Build` shall have signature:
    ```go
    func Build(system string, tools []core.Tool) string
    ```
19. If `system` is empty, `Build` shall start with `BaseInstructions` and append `UniversalGuidelines`.
20. If `system` is non-empty, `system` shall replace `BaseInstructions` and `UniversalGuidelines`.
21. Per-tool `PromptGuidelines` from `tools` shall be appended in first-seen order, deduplicated. Shell fallback guidelines (`ExecuteFallbackGuideline`, `SearchOverExecuteGuideline`) shall be added if applicable.
22. The types `prompt.Input`, `prompt.SkillBlocks`, and dependencies on `skills` shall be removed.

### Documentation Overhaul and Cleanup

23. `README.md` shall be rewritten to be under 200 lines and shall not mention any test function names:
    - Describe the two parts: "The hands" (workspace tools, outline, codesearch, codemode, mcp pool, shell guard) and "One driver" (`agentkit.Config`, `agentkit.New`, Anthropic wire).
    - Provide a concise 10-line driver example running prompt to completion.
    - Provide a code-mode example showing tool script delegation.
    - Present the reduced consumer contract table of kept identifiers.
24. `docs/architecture.md` shall be rewritten to document the post-cut architecture:
    - The package graph: everything imports `core`; `tools` imports `outline` and `schema`; `codemode` and `mcp` import `core` and `schema`; the root imports `tools`, `prompt`, `catalog`, and `provider/anthropic`; nothing imports the root.
    - Clear data flow of an execution turn.
25. `docs/configuration.md` shall document the complete runtime configuration surface:
    - `agentkit.Config` (14-15 fields).
    - `anthropic.Resolve` and its three credential/deployment environment variables (`ANTHROPIC_API_KEY`, `CLAUDE_CODE_USE_VERTEX`, `CLAUDE_CODE_USE_BEDROCK`).
    - `PruneOptions`, `codemode.Options`, `mcp.Config`.
    - Claude model catalog table.
26. `docs/DEPS.md` shall list the allowlist rulings for all approved direct dependencies:
    - `github.com/anthropics/anthropic-sdk-go`
    - `go.starlark.net`
    - `github.com/modelcontextprotocol/go-sdk`
    - `github.com/tree-sitter/go-tree-sitter` and grammars
    - `github.com/bmatcuk/doublestar/v4`
    - `github.com/pelletier/go-toml/v2`
    - Retain PRD 09's rejected library rulings.
27. Archive and deletions:
    - `docs/prd/agent-kit-prd.md` shall be moved to `docs/archive/agent-kit-prd.md` with a top header noting it is superseded by PRD 10.
    - `docs/archive/wires.md` shall record the findings from deleted providers and conformance test lessons.
    - `docs/api.md` (MCP server surface) and `docs/cli.md` (deleted command flags) shall be deleted.
28. Issues 91 (built-in skills prompt injection) and 92 (wall-clock test flakiness and outdated README claims) shall be marked closed.

## Design Decisions

1. **Delete `core/history.go` and `core/config.go` completely:** Unattended coding pipelines run fresh prompts to termination without resuming disk logs or managing branching histories. Moving driver configuration to `agentkit.Config` allows `core.AgentConfig` to be deleted, reducing exported vocabulary.
2. **Define `ProviderClient.Stream` returning `(<-chan StreamEvent, error)`:** Removing `ProviderRegistry`, `ProviderStreamOptions`, and `Middleware` simplifies the provider interface down to a single channel-producing method, making `provider/faux` and custom embedder test doubles trivial to implement.
3. **Fold ordered JSON parsing into `schema.Parse` and retire `jsonx`:** The only remaining need for order-preserving JSON decoding in the repository is maintaining property declaration order in JSON Schema objects. Folding this into `schema.Parse` using standard library `encoding/json/jsontext` removes the entire `jsonx` package and 360 lines of redundant code.
4. **Preserve `ToolUseBlock.Input` raw bytes for argument replay:** Retaining raw argument JSON bytes (`json.RawMessage`) from the model response guarantees byte-identical serialization across turns without requiring an intermediate `OrderedObject` data structure.
5. **Reshape `guard.Check` as a pure function on `argv []string`:** Program authorization logic should be decoupled from agent tool execution hooks. Exposing `guard.Check` allows host applications and custom tools to verify command safety directly, while `Restricted` provides a clean `BeforeToolCall` adapter.
6. **Eliminate image blocks from content models:** Unattended coding agents operate on text and code tokens. Deleting `ImageBlock` removes unnecessary union branches and prevents phantom media decoding failures across the loop.
7. **Prohibit test function citations in documentation:** Citing specific test function names in `README.md` creates documentation drift whenever tests are refactored or deleted (issue 92). The README shall focus on verifiable contracts and examples instead of test inventories.

## Dependencies

| Spec | Status | Reason |
|---|---|---|
| `09_repository_cut` | active | Removes obsolete packages (`session`, `skills`, `compaction`, `middleware`, `stop`), removes dead tools, and renames module path. |
| `10_anthropic_wire` | active | Rewrites `provider/anthropic` over official SDK, provides `anthropic.Resolve`, Claude `catalog.Lookup`, `Effort`, strict tool declarations, and cache breakpoints. |
| `11_minimal_driver` | active | Establishes `agentkit.Config` and minimal driver loop with prefix caching breakpoints, transcript pruning, batch execution, and termination handling. |
| `07_nested_tool_calls` | active | Defines `ReachableTools`, `NestedCaller`, and wrapper resolution semantics preserved in `core`. |
| `08_code_mode` | active | Defines Starlark code mode which validates tool arguments against `schema` and runs nested calls. |

## Verified External API

This specification interacts with the standard library `encoding/json/jsontext` available in the project's Go 1.27 environment.

### `encoding/json/jsontext` (verified from `wire/decode.go`)
```go
package jsontext

type Decoder struct {}
func NewDecoder(r io.Reader, opts ...Options) *Decoder
func (d *Decoder) ReadToken() (Token, error)
func (d *Decoder) PeekKind() Kind

type Token struct {}
func (t Token) Kind() Kind
func (t Token) String() string

type Kind rune
const (
    Null Kind = 'n'
    Bool Kind = 't'
    Number Kind = '0'
    String Kind = '"'
    ObjectStart Kind = '{'
    ObjectEnd Kind = '}'
    ArrayStart Kind = '['
    ArrayEnd Kind = ']'
)
```
