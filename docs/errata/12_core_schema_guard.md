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
