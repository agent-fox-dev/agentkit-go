# Erratum: spec 06 `tool_output_schemas`

Where the delivered output schemas and MCP result mapping differ from
`.specs/06_tool_output_schemas`, and why.

## 06-REQ-3.4: `Text` is set on a successful MCP call, not on an `isError` one

**Spec.** For any MCP tool execution, `ToolResult.Text` is the concatenated
text content blocks.

**Code.** `core.ToolResult` documents that an error result leaves `Text`
empty, so the model reads the envelope with `ok: false`, the error code and
the detail. A text-only error would lose the code. `mcp/pool.go` sets
`out.Text = text.String()` only on the success path. An `isError` result
keeps `Error: "tool_error"` and carries the joined text as `Detail`.

**Delivered.** TS-06-10 (`mcp/mcp_test.go`,
`TestTextIsTheJoinedTextBlocks_TS06_10`) checks `Text` over generated
content blocks on successful calls.

## Output schemas describe the Data the tools return today

The spec gives each built-in tool's Data shape from its PRD. For several tools
that shape is not what the code returns. The PRD's intent is to declare
existing shapes, not change them, so each schema describes what the tool
marshals. Renaming Data keys would change the result for every existing
programmatic consumer, and that is a change of its own.

### 06-REQ-5.1: `search_files` context keys are `before` and `after`

**Spec.** Match objects have optional `context_before` and `context_after`.

**Code.** `tools.SearchMatch` tags them `json:"before,omitzero"` and
`json:"after,omitzero"` (`tools/search.go`). The schema declares `before` and
`after`. Test: TS-06-14, `TestOutputSchemaSearchFiles_TS06_14`.

### 06-REQ-5.2: `file_outline`'s `file` object uses Go field names

**Spec.** `file` has `path`, `lang`, `backend` and `decls`, and each decl has
`name`, `kind`, `signature`, `start_line`, `end_line`, `container` (optional)
and `exported` (optional).

**Code.** `outline.File` and `outline.Decl` have no json tags, so `Data["file"]`
marshals as `Path`, `Lang`, `Backend`, `Decls`, and each decl as `Kind`, `Name`,
`Container`, `Signature`, `Exported`, `StartLine`, `EndLine`, all always
present. `declOutputSchema` in `tools/outline_tool.go` declares exactly that.
Test: TS-06-15, `TestOutputSchemaFileOutline_TS06_15`.

### 06-REQ-6.1: `find_symbol`'s `backends` is a count per backend

**Spec.** `backends` is an array of strings. Symbols have optional
`signature`, `container`, `exported` and `score`.

**Code.** `backends` is a `map[string]int` (`tools/symbol_tool.go`), declared
as an object whose additional properties are integers. `tools.SymbolMatch`
has no `score`, and it has a `backend` field. `signature`, `container` and
`exported` have no `omitempty`, so all nine fields are required. Test: TS-06-16,
`TestOutputSchemaFindSymbol_TS06_16`.

### 06-REQ-6.2: `find_references`' `target` is a declaration and `errors` a count

**Spec.** `target` is a string. Sites have `path`, `line`, `text`, and optional
`container` and `kind`. `errors` is an array of strings.

**Code.** `renderReferencesResult` (`tools/ref_render.go`) puts
`ReferenceResult.Target`, an `outline.Decl`, in `target`. `errors` is
`ReferenceResult.Errors`, an `int`. `sites` are `ReferenceSite`s with
`Path`, `Line`, `Column`, `Confidence`, `Enclosing` and `Source`.
`result` is the whole `ReferenceResult`. None of these types has json tags.
The schema in `tools/find_references.go` declares these keys. Test:
TS-06-17, `TestOutputSchemaFindReferences_TS06_17`.

### 06-REQ-8.1: `fetch_url` also declares `body`

**Spec.** `status`, `url`, `content_type`, `headers`, `truncated`, and optional
`binary` and `bytes`.

**Code.** A text response sets `data["body"]` (`tools/fetch.go`), and a binary
one sets `binary` and `bytes` instead. The schema adds an optional `body`
string. `headers` is declared as an object of strings. Test: TS-06-19,
`TestOutputSchemaFetchURL_TS06_19`.

### 06-REQ-8.2: `code_search` files carry a match count and context chunks

**Spec.** Each file has `path` and `matches`, an array of line objects with
`context_before` and `context_after`. `symbol_sources` and `dirty_files` are
arrays of strings.

**Code.** `executeCodeSearch` (`codesearch/tool.go`) builds each file as
`path`, `score` (number), `matches` (an integer count), `chunks` (each
`{lines: [{line, text, match}]}`) and `symbols`. `symbols` is null when no
declaration starts on a matched line. `symbol_sources` is a `map[string]int`
of files per backend, and `dirty_files` is an `int`. `codeSearchOutputSchema`
declares exactly that. Test: TS-06-20, `TestOutputSchemaCodeSearch_TS06_20`.

## 06-REQ-9.8: `edit_file` reports `edit_<phase>`, and `edit_failed` cannot be induced

**Spec.** `edit_file` documents `edit_precondition` and `edit_failed`.

**Code.** No tool returns `edit_precondition`. When `ApplyEdits` rejects a
batch, `edit_file` returns `"edit_" + ee.Phase` (`tools/tools.go`). The phases
come from `tools/edit.go`: `edit_empty`, `edit_empty_old_string`,
`edit_not_found`, `edit_not_unique`, `edit_overlap` and `edit_noop`.
`edit_failed` is returned only for an error from `ApplyEdits` that is not an
`*EditError`, and `ApplyEdits` returns no other kind. That branch is a
backstop that no input can reach.

**Delivered.** TS-06-29 (`tools/conformance_test.go`,
`TestErrorCodesFailures_TS06_29`) asserts `edit_not_found` for an
`old_string` the file does not contain. It does not induce `edit_failed`.

## Test layout

- **D-4.** `code_search` is in the nested `codesearch` module, which the root
  module does not depend on. Its conformance tests (TS-06-20, and its share of
  TS-06-22 and TS-06-23) are in `codesearch/tool_test.go`. Everything else is
  in `tools/conformance_test.go`. That file is the external `tools_test`
  package, so it can import `subagent` without an import cycle.
- The test spec names constructors such as `tools.ReadFileTool(ws)` that do
  not exist. The built-in tools are reached through `tools.All`,
  `tools.FetchTool` and `subagent.Tool`.
