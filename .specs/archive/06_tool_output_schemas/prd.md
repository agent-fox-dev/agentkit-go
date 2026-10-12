---
spec_id: "06"
spec_name: "tool_output_schemas"
title: "Tool Output Schemas, Structured MCP Results, and Schema Conformance"
status: "active"
created_at: "2026-10-08T14:39:31.060914Z"
updated_at: "2026-10-08T14:39:31.060914Z"
intent_hash: "ef73894c4260d9b22ace2080048d376370d2882fb43bd8ff707dedbafc77ce90"
schema_version: 2
source: "docs/prd/08-run-tool-calls-from-model-written-scripts.md"
---
## Intent

Allow tools to declare the schema of their structured output (`Data`) so that downstream programmatic callers—including future script runners and wrapper tools—can safely bind and interpret tool results. Every built-in tool declares an output schema verified by conformance tests, MCP server output schemas and structured results are passed through, and provider request bodies remain byte-identical.

## Goals

1. Any `core.Tool` can declare an optional `OutputSchema *schema.Schema` describing the structured result (`Data`) it produces.
2. The request bodies across all five provider wire APIs (Anthropic, Google, Ollama, OpenAI, OpenAI Responses) remain byte-identical: `core.ToolWire` excludes `OutputSchema`, and all wire golden tests pass unchanged.
3. Every built-in tool (`read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`, `fetch_url`, `execute`, `run_command`, `powershell`, `code_search`, and `subagent`) declares an `OutputSchema`.
4. A conformance test suite validates that every built-in tool returns a `ToolResult.Data` payload matching its declared `OutputSchema` on success, and verifies each documented error code on failure.
5. Tools imported from an MCP pool carry the server's `outputSchema` as `OutputSchema`. A malformed schema from an MCP server is dropped with a diagnostic recording the server and tool name, without failing the connection.
6. MCP tool execution passes the server's `structuredContent` directly into `ToolResult.Data` when present, falling back to text content when absent.

## Non-goals

- **No runtime validation of tool results against output schemas.** Output schemas document return shapes for consumers and static description generation; validating every result at runtime would turn schema drift into tool failures.
- **No modification of provider wire requests.** Output schemas are purely for internal consumers, script harnesses, and documentation. They are never sent to LLM providers.
- **No changes to model-facing text rendering.** `ToolResult.Text` remains the unescaped, human-readable text that LLMs receive; `ToolResult.Data` carries the structured object described by `OutputSchema`.
- **The rest of the script-running feature lands in later specs of this split:**
  - `nested_tool_calls`: declaring reachable tools on `core.Tool`, cycle detection, policy resolution through wrappers, unguarded shell checks across wrappers, transitive reachable-set inspection, and the nested tool execution dispatch pipeline (interceptor context with parent tool-use ID, blocked call handling, terminate vote semantics, concurrency, events, and audit logging).
  - `code_mode`: the sandboxed Starlark script runner tool (`go.starlark.net`), keyword argument function bindings, concurrent execution primitives, runtime step/time/call/output limits, output truncation/spill, and prompt documents.

## Background

The product proposal (`docs/prd/08-run-tool-calls-from-model-written-scripts.md`) defines a mechanism for running tool calls from model-written scripts. The foundation of this capability is typed tool contracts: in order for a script runtime to present tools as typed functions and inspect their results, tools must expose not only what arguments they accept (`InputSchema`), but also the shape of the structured data they return (`Data`).

What exists today:

- `core.Tool` (`core/tool.go`) defines `InputSchema *schema.Schema` but has no field for output schemas.
- `core.ToolWire` (`core/tool.go`) is the provider-facing projection containing only `Name`, `Description`, `InputSchema`, and `ConstrainedSampling`. `canonicalRequest` in `golden_requests_test.go` verifies serialization against goldens in `testdata/golden/request_*.json`.
- `core.ToolResult` (`core/tool.go`) already distinguishes `Text` (the unescaped text the model reads) from `Data map[string]any` (structured fields for programmatic consumers) and `Metadata *ToolMetadata` (execution metadata).
- In `tools/tools.go`, `tools/search.go`, `tools/outline_tool.go`, `tools/symbol_tool.go`, `tools/find_references.go`, `tools/fetch.go`, `codesearch/tool.go`, and `subagent/subagent.go`, each built-in tool populates `Data` with structured information (e.g. `read_file` returns `content` and `encoding`; `list_files` returns `entries` and `truncated`; `execute` returns `output`, `exit_code`, and `outcome`), but none of these shapes are formally declared as a `*schema.Schema`.
- In `mcp/pool.go`, `adapt` converts an MCP server tool definition `*sdk.Tool` into a `core.Tool`. It maps `InputSchema` using `schemaFrom`, but ignores `OutputSchema`. When calling the tool, `adapt` captures `res.StructuredContent` into `data["structured"]`, but does not expose it as the primary result object matching the server's schema.
- Non-fatal diagnostics in the codebase use `diag.Diagnostic` (`internal/diag/diag.go`), used by skill manifests, plugin loaders, and MCP config parsing (`mcp/config.go`).

## Requirements

### Output Schema on Tools

1. **`core.Tool` declaration:**
   - `core.Tool` gains `OutputSchema *schema.Schema`.
   - `OutputSchema` is optional: tools that leave it `nil` behave exactly as before.
   - `core.ToolWire` does not include `OutputSchema`. Provider request serialization and wire request golden tests remain unchanged and byte-identical across Anthropic, Google, Ollama, OpenAI, and OpenAI Responses wire formats.

### MCP Client Output Schema and Structured Content

2. **MCP tool output schema mapping:**
   - In `mcp/pool.go`, `adapt` parses the MCP tool's `OutputSchema` using `schemaFrom` when provided by the server.
   - If the server provides a malformed `OutputSchema` (unparseable JSON or schema converter failure), the schema is dropped (`OutputSchema` is set to `nil`), and a diagnostic of severity `SeverityError` naming the server and tool is appended to the pool's diagnostics. The connection and tool import succeed without error.
   - `mcp.Pool` exposes `Diagnostics() []diag.Diagnostic` to return collected non-fatal diagnostics.

3. **MCP tool structured result pass-through:**
   - When an MCP tool execution returns a non-nil `StructuredContent`:
     - If `StructuredContent` is a JSON object (`map[string]any`), `core.ToolResult.Data` is set directly to that object.
     - If `StructuredContent` is a non-object JSON value, `core.ToolResult.Data` is set to `map[string]any{"value": StructuredContent}`.
   - When `StructuredContent` is nil or absent:
     - `core.ToolResult.Data` is set to `map[string]any{"text": text.String(), "content": content}`.
   - `core.ToolResult.Text` continues to carry the joined text content for the model.

### Built-in Tool Output Schemas

4. **File operations tools (`tools/tools.go`):**
   - `read_file`: Declares `OutputSchema` as `schema.OneOf`:
     - Text read: `schema.Object(schema.Prop("content", schema.String()), schema.Prop("encoding", schema.String()))`.
     - Image read: `schema.Object(schema.Prop("note", schema.String()), schema.Prop("mime_type", schema.String()), schema.Prop("width", schema.Int()), schema.Prop("height", schema.Int()))`.
   - `write_file`: Declares `OutputSchema`:
     - `schema.Object(schema.Prop("written", schema.Bool()), schema.Prop("bytes", schema.Int()))`.
   - `edit_file`: Declares `OutputSchema`:
     - `schema.Object(schema.Prop("edits_applied", schema.Int()))`.
   - `list_files`: Declares `OutputSchema`:
     - `schema.Object(schema.Prop("entries", schema.Array(schema.String())), schema.Prop("truncated", schema.Bool()), schema.Opt("note", schema.String()))`.
   - `find_files`: Declares `OutputSchema`:
     - `schema.Object(schema.Prop("files", schema.Array(schema.String())), schema.Prop("truncated", schema.Bool()), schema.Opt("marker", schema.String()))`.

5. **Search and outline tools (`tools/search.go`, `tools/outline_tool.go`):**
   - `search_files`: Declares `OutputSchema` with properties:
     - `matches`: array of objects (`file` string, `line` integer, `text` string, optional `context_before` array of string, optional `context_after` array of string).
     - `truncated`: boolean.
     - `files_searched`: integer.
     - `note`: optional string.
   - `file_outline`: Declares `OutputSchema` with properties:
     - `file`: object (`path` string, `lang` string, `backend` string, `decls` array of decl objects with `name` string, `kind` string, `signature` string, `start_line` integer, `end_line` integer, optional `container` string, optional `exported` boolean).
     - `backend`: string.
     - `declarations`: integer.
     - `listed`: integer.

6. **Symbol and reference tools (`tools/symbol_tool.go`, `tools/find_references.go`):**
   - `find_symbol`: Declares `OutputSchema` with properties:
     - `symbols`: array of symbol match objects (`name` string, `kind` string, `path` string, `start_line` integer, `end_line` integer, optional `signature` string, optional `container` string, optional `exported` boolean, optional `score` number).
     - `truncated`: boolean.
     - `backends`: array of string.
     - `files_indexed`: integer.
     - `note`: optional string.
     - `partial`: optional boolean.
     - `partial_reason`: optional string.
   - `find_references`: Declares `OutputSchema` with properties:
     - `target`: string.
     - `sites`: array of reference site objects (`path` string, `line` integer, `text` string, optional `container` string, optional `kind` string).
     - `backend`: string.
     - `partial`: boolean.
     - `truncated`: boolean.
     - `packages_checked`: integer.
     - `errors`: array of string.
     - `result`: object.

7. **Subprocess and shell tools (`tools/exec.go`, `tools/tools.go`, `tools/powershell.go`):**
   - `execute`, `run_command`, and `powershell`: Shared subprocess result schema declared via `execResultOutputSchema()`:
     - `schema.Object(schema.Prop("output", schema.String()), schema.Prop("exit_code", schema.Int()), schema.Prop("outcome", schema.Enum("ok", "exit", "signal", "timeout", "abort")))`.
   - On exit status non-zero or failure outcome, `core.ToolResult.Data` continues to be populated with `output`, `exit_code`, and `outcome`, conforming to this schema.

8. **Network and index tools (`tools/fetch.go`, `codesearch/tool.go`, `subagent/subagent.go`):**
   - `fetch_url`: Declares `OutputSchema` with:
     - `status`: integer.
     - `url`: string.
     - `content_type`: string.
     - `headers`: object.
     - `truncated`: boolean.
     - `binary`: optional boolean.
     - `bytes`: optional integer.
   - `code_search`: Declares `OutputSchema` with:
     - `files`: array of file matches (`path` string, `matches` array of line match objects with `line` integer, `text` string, optional `context_before` array of string, optional `context_after` array of string).
     - `truncated`: boolean.
     - `note`: string.
     - `partial`: boolean.
     - `partial_reason`: string.
     - `symbol_sources`: array of string.
     - `files_indexed`: integer.
     - `dirty_files`: array of string.
     - `skipped`: object (`binary` int, `oversized` int, `too_many_trigrams` int, `too_small` int).
   - `subagent`: Declares `OutputSchema` with:
     - `result`: string.
     - `turns`: integer.

### Conformance Testing

9. **Built-in tool schema conformance tests:**
   - A conformance test suite executes every built-in tool:
     - On successful execution, the test verifies `res.OK == true`, serializes `res.Data` into `jsonx.OrderedObject`, and asserts `schema.Validate(tool.OutputSchema, ordered) == nil`.
     - On documented error paths for each tool, the test induces the error, asserts `res.OK == false`, and verifies that `res.Error` matches the documented error code. If the tool produces `res.Data != nil` on error (as subprocess shell tools do), `res.Data` is validated against `tool.OutputSchema`.
   - Documented error codes verified include:
     - `read_file`: `invalid_arguments`, `path_not_allowed`, `not_a_file`, `read_failed`, `offset_past_end`.
     - `write_file`: `invalid_arguments`, `path_not_allowed`, `write_failed`.
     - `edit_file`: `invalid_arguments`, `path_not_allowed`, `read_failed`, `not_a_file`, `edit_precondition`, `edit_failed`.
     - `list_files`: `invalid_arguments`, `path_not_allowed`, `list_failed`.
     - `find_files`: `invalid_arguments`, `path_not_allowed`, `aborted`.
     - `search_files`: `invalid_arguments`, `path_not_allowed`, `aborted`.
     - `file_outline`: `invalid_arguments`, `path_not_allowed`, `read_failed`, `not_a_file`, `outline_failed`, `aborted`.
     - `find_symbol`: `invalid_arguments`, `path_not_allowed`, `aborted`.
     - `find_references`: `invalid_arguments`, `aborted`.
     - `fetch_url`: `invalid_arguments`, `scheme_not_allowed`, `address_not_allowed`.
     - `execute` / `run_command` / `powershell`: `invalid_arguments`, `exec_failed`, `command_exit`.
     - `code_search`: `invalid_arguments`.
     - `subagent`: `invalid_arguments`, `subagent_failed`.

## Design Decisions

1. **`OutputSchema` is on `core.Tool` and excluded from `core.ToolWire`.** This ensures provider request bodies are byte-identical and goldens remain unaltered while giving internal callers full access to output schema metadata.
2. **`OutputSchema` is optional and not enforced at runtime.** Enforcing output schema validation on every tool call in production would turn minor schema drift into fatal execution bugs; conformance tests guarantee accuracy during testing instead.
3. **MCP `structuredContent` maps directly to `ToolResult.Data` when present.** An MCP server's `outputSchema` describes the shape of its `structuredContent`; setting `Data` directly to the `structuredContent` map ensures `Data` adheres to the declared `OutputSchema`.
4. **Malformed MCP output schemas produce non-fatal diagnostics.** Dropping malformed output schemas and appending a diagnostic to `mcp.Pool` follows the lenient parsing convention used in `skills/` and `plugins/`, allowing the tool to remain usable without output typing.
5. **`read_file` output schema uses `schema.OneOf` for text and image variants.** Because `read_file` sniffs magic bytes and dynamically returns either file text or image metadata blocks, a union schema accurately documents both valid return shapes.
6. **Subprocess shell tools share a single output schema definition.** `execute`, `run_command`, and `powershell` all produce output through `execResultToTool`; defining one canonical schema prevents drift across shell variants.
7. **`ToolResult.Data` on error paths is validated only when populated.** Subprocess tools return structured output (`output`, `exit_code`, `outcome`) even on non-zero exit codes; other tools return `Data == nil` with `Error` and `Detail`. Conformance tests validate `Data` when non-nil and assert `res.Error` across all documented error codes.

## Dependencies

| Spec | Why this spec depends on or modifies it |
|---|---|
| `01_outline_and_walk` | Adds output schema declarations and conformance tests for `file_outline`. |
| `02_symbol_navigation_tools` | Adds output schema declarations and conformance tests for `find_symbol`. |
| `03_indexed_code_search` | Adds output schema declarations and conformance tests for `code_search`. |
| `04_runner_and_tool_metadata` | Adds output schema declarations and conformance tests for shell tools (`execute`, `run_command`, `powershell`) matching `ToolMetadata` shapes. |
| `05_find_references` | Adds output schema declarations and conformance tests for `find_references`. |

## Verified External API

| Package | Symbol | Real Signature / Implied Shape | Status |
|---|---|---|---|
| `github.com/modelcontextprotocol/go-sdk/mcp` | `Tool.OutputSchema` | `any` (JSON Schema object/raw JSON from server) | verified (implied by `mcp.Tool` alias in `mcp/client.go` and MCP 2025-06-18 spec) |
| `github.com/modelcontextprotocol/go-sdk/mcp` | `CallToolResult.StructuredContent` | `any` (decoded JSON object or primitive) | verified (read in `mcp/pool.go` line 333) |
| `github.com/modelcontextprotocol/go-sdk/mcp` | `CallToolResult.Content` | `[]Content` | verified (read in `mcp/pool.go` line 320) |
| `github.com/modelcontextprotocol/go-sdk/mcp` | `CallToolResult.IsError` | `bool` | verified (read in `mcp/pool.go` line 336) |
