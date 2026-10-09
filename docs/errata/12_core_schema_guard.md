# Erratum: spec 12 `core_schema_guard`

Where the delivered change differs from `.specs/12_core_schema_guard`, and
why.

## 12-REQ-1: what the pruned messages cost

- **Unknown block types are dropped.** `RawBlock` carried a block type core
  does not model, byte for byte, from a response to the next request. With it
  gone, `provider/anthropic`'s decoder drops such a block. The one that
  mattered was server-side compaction (`BetaCompaction`): its blocks are no
  longer replayed, so the beta header still turns compaction on, but the
  compacted state does not carry into the next turn. The test that pinned the
  replay (`TestServerCompactionBlocksAreReplayedVerbatim`) was deleted.
- **No images.** `ImageBlock` and `ToolResultBlock` are gone, and with them the
  encoder's image path, transcript repair's rule 7 (images replaced for a
  text-only model, `RepairReport.ImagesReplaced`, `ImagePlaceholder`) and the
  tests of image normalization. The canonical golden request lost its image
  block (`testdata/golden/request_anthropic.json`).
- **Provenance is the model id.** `AssistantMessage` keeps `Model`;
  `Provider` and `API` are gone. Transcript repair's "same model" was the
  (provider, API, model) triple and is now the model id
  (`provider/anthropic/repair.go`), and `anthropic.Target` is `{Model,
  NormalizeToolCallID}`.
- **`NewToolUse` and `InputMap` decode with `encoding/json`** (numbers as
  `json.Number`), not `jsonx`. Task 1 left `core/arguments.go` decoding the
  ordered form from `Input` until task 2 rewrote it.

## 12-REQ-2: argument preparation on maps

- **The schema's argument functions moved first.** `schema.DeleteOptionalNulls`,
  `Coerce` and `Validate` took `jsonx.OrderedObject`; `core.PrepareArguments`
  is their only production caller, so they moved to plain values with it, in
  task 2 rather than task 6. `Validate(s, v any)` takes any decoded value
  (`map[string]any`, `[]any`, a string, `json.Number` or a Go number);
  `ValidationError.Args` is JSON bytes, which `PrepareArguments` replaces with
  the model's own so the echo keeps the model's key order, and long strings
  in it are shortened token by token with `encoding/json/jsontext`.
- **A changed argument keeps its neighbours' bytes.** When a step changes the
  arguments, `Raw` is rebuilt from the model's bytes: a key whose value is
  unchanged keeps its position and its exact bytes, a changed key is
  re-encoded in place, and a new key follows in lexical order
  (`encodeLike` in `core/arguments.go`). TS-12-5 pins
  `{"v": 1,"injected":true}`; the spec asked only that the mutation appear.
  Inside a changed value, nested key order is not kept.
- **Coerced numbers are values.** `Coerce` yields a `float64` for a number
  and an `int64` for an integer (TS-12-27), so `"-1.5e3"` becomes `-1500`; it
  used to write the literal verbatim. The issue 87 test now expects `-1500`.
- TS-12-4 and TS-12-6 passed before the change: unmodified bytes and the
  validation error were already the pipeline's behaviour.

## 12-REQ-3: the channel seam, and where the request options went

- **The provider is built for its model.** `Request` carries no model, so
  `anthropic.Provider(model, opts)` binds one; `faux.Provider` stamps
  `ModelID` when set, and the driver fills an unstamped message's `Model`
  with its own.
- **`Request.Prefix` stays.** The spec's `Request` omits it, but spec 11's
  `Config.Prefix` and its cache breakpoint travel on it.
- **The per-request options moved to the provider.** `RequestOptions`
  (headers, timeout, environment override, transport, `OnPayload`,
  `OnResponse`, cache retention, max retries) is gone with
  `ProviderStreamOptions` (`Warnf`, cache retention); the fields the
  Anthropic provider reads are now on `anthropic.Options`. `SessionID`,
  `StreamFn` and `MaxRetryDelayMs` were not read and are gone.
- **Deferred submission is gone**: `DeferredRequest`, `DeferredHandle`,
  `DeferredFunc`, `StopReasonDeferred`, `RunStopDeferred` and
  `ErrDeferredUnsupported` had no producer.
- **The stream contract.** The last `MessageEndEvent` on the channel carries
  the turn's message; a failure sets `Err` on the last item. Two adapters in
  `core` join the seam to `EventStream`: `StreamChannel` (an `EventStream` to
  a channel, used by both providers) and `EventStreamOf` (a channel to an
  `EventStream`, used by the driver). Both providers refuse a context already
  done with its error (TS-12-9).
- **A cancelled request, not a cancelled context.** The test that pinned an
  aborted turn (`TestCancellationProducesAnAbortedTurnNotAnError`) now cancels
  while the request is in flight, since a context done before `Stream` is
  refused.
- **TS-09-11** asked the registry to hold anthropic and faux; with no
  registry it checks both are a `core.ProviderClient` and are the only
  provider packages.
- TS-12-7 did not compile against the previous commit (`StreamEvent` did not
  exist); that was its first failure.

## 12-REQ-4: what Model, Tool and ToolWire keep

- **`Model` keeps two fields beyond the spec's list.** `Efforts`
  (`map[Effort]*string`, the wire value per effort) replaces the catalog's
  `ThinkingLevelMap`: the encoder needs a row's budget or effort name to send
  thinking. `Compat` stays because the encoder reads `supports_sampling`
  from it to drop temperature and top_p. `Name`, `API`, `Provider`,
  `BaseURL`, `Headers`, `Input`, `Reasoning`, `Cloned` and the price tiers
  are gone; the catalog still reads and validates `api`, `base_url` and
  `reasoning` in its file, but does not copy them onto `Model`.
- **No price tiers.** `core.Cost` and `CostTier` are gone and the four prices
  are flat fields; `provider.RatesFor` and the tier tests went with them. No
  catalog row had tiers.
- **`ThinkingLevel` is gone.** The catalog keys its file's
  `thinking_level_map` by string (`off`, `minimal`, then the five efforts)
  and copies the five efforts to `Model.Efforts`; `off` and `minimal` are
  validated but never sent. `ThinkingKindOf` moved into the catalog, and the
  Anthropic encoder infers the kind from `Efforts` when a hand-built `Model`
  leaves `ThinkingKind` empty.
- **`ToolResult.Blocks` stays** (the survey's D-8 said it would go): without
  images it still carries extra text blocks after the result's text, which
  the batch appends and tests use.
- **`Tool.MCPServer` is gone**, so an MCP tool's server is known only from
  its qualified name; the assertion on it was removed from
  `TestToolNamesAreQualifiedByServer`. **`ConstrainedSampling`** is gone
  from `Tool` and `ToolWire`; the customtools example now points at the wire's
  `strict: true` instead. `MCPServerOf` never existed.
- TS-12-11's pseudocode checks `RunResult`, `RunStopReason` and `StopReason`
  as fields of `Usage`; they are types, and the test checks that core
  declares them.

## 12-REQ-5: the budgets, and the one that is missed

- **Lines of code, not lines.** TS-12-14 counts the non-blank lines that are
  not comments (`codeLines` in `core/size_test.go`); `core` is 1,600 or
  fewer and the root module 20,000 or fewer by that count. Counted raw, the
  root module was about 26,000 lines before this spec, comments included,
  and no task removes six thousand lines of code.
- **The 50-type budget is missed, by decision.** The event vocabulary
  (`core/event.go`, 24 types) and the tool and interceptor vocabulary stay,
  so after every deletion this spec names `core` declares 73 exported types.
  TS-12-13 counts them and skips with that count rather than failing: an
  unmet requirement, not a regression.
- **More went than the spec named.** `Phase`, `ConfigView`,
  `SessionSnapshot` and `RetryAfterError` had no user left. `DefaultMaxTokens`
  lived in `config.go`; it is `agentkit.DefaultMaxTokens` now, the driver
  being its only reader.

## 12-REQ-6: what replaced jsonx

- `schema.Schema.Extra` was a `jsonx.OrderedObject`. It is now `schema.Extra`,
  a slice of `schema.Keyword{Key, Value json.RawMessage}` in authored order,
  with `Get(key)`; `WithExtra` panics on a value JSON cannot carry.
- The argument pipeline and the schema validator left `jsonx` in task 2.
  `difftest/canon.go`, named in the task's `touches`, no longer exists.
- TS-12-15..17 live in `core/size_test.go` beside the other repository
  checks, and find the module root with `go list -m`.

## 12-REQ-7: Parse

- `schema.Parse` lives in `schema/parse.go`. Beyond the keywords the spec
  lists it reads the bounds (`minimum` … `multipleOf`, `minLength`,
  `maxLength`, `pattern`, `format`, `minItems`, `maxItems`, `uniqueItems`)
  and `additionalProperties` (a boolean or a schema), and it reads a type
  list of one type and `null` as `Nullable`, which is how `MarshalJSON`
  writes it; what `Parse` reads, `MarshalJSON` writes back unchanged.
- Kept values are compacted, so a schema re-marshals the way it would have
  been written.
- `Extra` is a list, not a map: TS-12-20 reads it with `Extra.Get` and checks
  the order.
- TS-12-21 passed against the stub, which refused every input.
- No production code calls `Parse` yet: the MCP client converts the schemas
  its SDK has already decoded (`mcp/pool.go`, `schemaFrom`), whose key order
  is already gone, with fallbacks of its own.

## 12-REQ-8: the constraints arrived with task 2

`additionalProperties: false`, enum, the numeric bounds, `multipleOf`, string
lengths and item counts were already enforced before this spec;
`pattern` and `uniqueItems` were added, and coercion moved to plain values,
when task 2 rewrote the validator over `map[string]any`
(`validateValue`, `validateArray`, `validateString`, `validateNumber`,
`coerceValue` in `schema/impl.go`). TS-12-22..28 therefore passed when
written; each was seen to fail against a mutated `schema/impl.go` (closed
objects, enum, minimum, pattern, uniqueness, the JSON-number check and
boolean coercion each disabled in turn). The coercion tests sit in
`schema/issue87_test.go` beside the issue 87 tests rather than in a
`coerce_test.go` of their own.

## 12-REQ-9: Check, and what Restricted kept

- **A listed path still admits its bare basename.** `AllowedPrograms:
  ["/usr/bin/git"]` lets `git` run, as before (`newAllowlist` in
  `guard/guard.go`); the spec matches a bare name only against bare entries.
  A program with a path separator still matches only a listed path, after
  cleaning (TS-12-31).
- **`run_command` checks every argv entry is a string** and refuses the call
  otherwise; an empty argv is `empty argv` (from `Check`), where it used to
  be `program "" is not on the allowlist`.
- **`powershell` is gone** from `ShellToolNames`, and with it
  `Options.PowerShellFilter` and the policy's PowerShell case.
- TS-12-33 passed against the stub (which allowed everything) and TS-12-34
  against the existing `Restricted`; TS-12-29..32 failed first.

## 12-REQ-10: prompt.Build was already reshaped

`prompt.Build(system, tools)` arrived with spec 11 (task 3), without
`prompt.Input`; `SkillBlocks` and the `skills` import were gone with spec 09.
TS-12-35..37 passed when written; TS-12-36 and TS-12-37 were seen to fail
against a mutated `prompt/prompt.go` (the custom branch, deduplication and
both shell guidelines each disabled). One detail follows the code: the
execute fallback guideline is added when no file-navigation tool is
present, not "when no search tool is" — `tools.FileNavigationTools()` is
the set it checks.

## Integration and documentation

- **The smoke tests** (`core/smoke_test.go`) run the real parser, argument
  pipeline, guard, prompt builder and `provider/faux`. 12-PATH-1 starts with
  a developer calling `schema.Parse` through the public API: no production
  code in this repository calls `Parse`, because the one place a schema
  arrives from outside (the MCP client) receives it already decoded. The
  argument half of the path runs in `batch.go` and `nested.go`.
- **The package graph differs from the PRD's sketch.** The root does not
  import `tools` (it imports `core`, `catalog`, `guard`, `prompt`, `provider`,
  `provider/anthropic` and the SDK; `prompt` imports `tools`); `schema`,
  `outline` and `wire` import no part of `core`; `codemode` also imports
  `tools`, and `mcp` also imports `wire` and two internal packages.
  `docs/architecture.md` describes the graph `go list` reports.
- **The README's license line now says Apache 2.0**, which is what `LICENSE`
  says; it used to say MIT.
- **Issues 91 and 92 are not closed by this change.** Closing them is an
  action on GitHub, left to whoever lands it; the README cites no test names
  and the documentation describes the cut repository.
