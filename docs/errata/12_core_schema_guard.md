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
