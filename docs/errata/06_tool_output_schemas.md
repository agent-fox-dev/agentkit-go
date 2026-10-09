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
