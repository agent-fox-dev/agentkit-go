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
