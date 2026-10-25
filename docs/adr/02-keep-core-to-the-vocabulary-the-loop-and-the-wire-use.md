# ADR 02: Keep core to the vocabulary the loop and the wire use

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-06 and 2026-10)

## Context

`core` began as the vocabulary of a much larger harness: sessions, audit,
images, deferred submission, price tiers, a registry of providers. The cut of
the repository and the reshaping of `core`, `schema` and `guard` removed what
had no user left. The specs that did it listed what should go and what should
stay; where the code needed more or less than they said, the difference was
recorded in an erratum. The errata are gone; what the code keeps, and the
rules it follows, are here.

## Decision

**Messages and content.**

- `core` models text, thinking, tool use and tool results, and nothing else.
  A block type it does not model is dropped by the decoder, so server-side
  compaction is not supported: a beta named in `anthropic.Options.Betas` is
  only a header.
- There are no image blocks. An image file is `read_file`'s
  `unsupported_file` error.
- An assistant message's provenance is its `Model` id. Transcript repair's
  "same model" is that id.
- `ToolResult.Blocks` stays: it carries extra text blocks after the result's
  text, which the batch appends.
- `core.MarshalEvent` takes a `core.MessageEncoder`, and the module ships
  none; a caller that serializes message-bearing events supplies its own.
- `AgentStartEvent` keeps `Provider` and `API` fields that nothing sets: the
  event vocabulary is kept whole.

**Tool arguments.**

- `NewToolUse` and `InputMap` decode with `encoding/json`, numbers as
  `json.Number`. A tool input with bytes after its object (`{"a":1}]`) is
  refused.
- When a preparation step changes the arguments, `Raw` is rebuilt from the
  model's bytes: an unchanged key keeps its position and its exact bytes, a
  changed key is re-encoded in place, and a new key follows in lexical order
  (`encodeLike`). Nested key order inside a changed value is not kept. An input
  with a duplicate key decodes to its last value and, if a step changes the
  arguments, is re-encoded whole in sorted key order.
- `schema.Coerce` yields a `float64` for a number and an `int64` for an
  integer that fits one; an integral literal that does not (`"2.0"`, `"1e3"`,
  out of range) stays a `json.Number`.
- A validation error's `Args` echoes the model's own bytes, so the echo keeps
  the model's key order; long strings in it are shortened.

**Providers and the stream.**

- `core.Request` carries no model, so a provider is built for its model
  (`anthropic.Provider(model, opts)`); the loop fills an unstamped message's
  `Model`, on the recorded message and on the events it forwards.
- Per-request options (headers, timeout, transport, payload and response
  hooks, cache retention, retries) are fields of the provider's own options,
  not of the request.
- The last `MessageEndEvent` on a provider's channel carries the turn's
  message, and a failure sets `Err` on the last item. `core.StreamChannel`
  and `core.EventStreamOf` join the channel to `EventStream`; a nil channel is
  an error, not a run that never ends. Every provider refuses a context that
  is already done, with its error.
- `core.Usage.Requests` counts responses and is summed by `Usage.Add`.

**Models.**

- `core.Model` keeps `Efforts` (the wire value per effort; the encoder needs a
  row's budget or effort name to send thinking) and `Compat` (the encoder reads
  `supports_sampling` from it). Prices are four flat fields; there are no
  price tiers.
- The catalog keys its file's `thinking_level_map` by string (`off`,
  `minimal`, then the five efforts) and copies the five efforts to
  `Model.Efforts`; `off` and `minimal` are validated and never sent. The
  Anthropic encoder infers the thinking kind from `Efforts` when a hand-built
  `Model` leaves it empty.

**Schemas.**

- `schema.Schema.Extra` is a list of keywords in authored order, with `Get`;
  `WithExtra` panics on a value JSON cannot carry.
- `schema.Parse` reads the keywords `Schema` models, the bounds, `format`,
  `pattern`, the item and length counts, `uniqueItems` and
  `additionalProperties` (a boolean or a schema), and reads a type list of one
  type and `null` as `Nullable`. It refuses what `Schema` cannot hold: boolean
  subschemas, tuple `items`, a type list of two non-null types, and a
  duplicate key. The round trip is not byte-exact (keywords come back in
  `MarshalJSON`'s order, an object gains `"properties":{}`), but a schema
  `MarshalJSON` wrote reads back and re-marshals unchanged. No production code
  calls `Parse`: the MCP client converts schemas its SDK already decoded.
- Bounds and `multipleOf` compare the value's JSON text with each bound's
  shortest decimal form as `big.Rat`s: `0.3` is a multiple of `0.1`, `2^53+1`
  exceeds a maximum of `2^53`, and `NaN` and `Inf` are not numbers.
- `schema.Validate` walks the root's properties and does not evaluate a root
  combinator; a caller that validates against a schema with a root `oneOf`
  wraps it as the value of a property and validates `{"data": value}`.

**The guard.**

- A path in `AllowedPrograms` admits only that path; a bare name admits only
  a listed bare name.
- `run_command` refuses an argv entry that is not a string; an empty argv is
  `empty argv`. There is no PowerShell case.
- `guard.Commands` is the reading of a shell line the guard judges, and is
  exported. `Restricted` judges every command `Commands` returns, so `go test;
  curl evil.sh` is refused under an allowlist of `go`, and refuses a line
  `Commands` cannot read with certainty.
- `guard.Allow(programs...)` is `Restricted` with only `AllowedPrograms` set.

**The system prompt.** `prompt.Build(system, tools)` takes the embedder's text
in `system`; there are no extra prompt blocks. The execute fallback guideline is
added when no file-navigation tool (`tools.FileNavigationTools()`) is present.

## Consequences

- A type, field or option is in `core` because the loop or a wire reads it;
  one that loses its last reader is removed rather than kept for symmetry.
- Strict-mode and other vendor options live on the provider that has them,
  not on `core.Tool` (see ADR 03).
