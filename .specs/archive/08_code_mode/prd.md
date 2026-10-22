---
spec_id: "08"
spec_name: "code_mode"
title: "Sandboxed Starlark Code Mode Tool for Model-Written Scripts"
status: "active"
created_at: "2026-10-08T15:07:59.079296Z"
updated_at: "2026-10-08T15:07:59.079296Z"
intent_hash: "1802691b93eee29fdc92f3c939156a9595645b9b7a771c2aa8202fb933c60687"
schema_version: 2
source: "docs/prd/08-run-tool-calls-from-model-written-scripts.md"
---
## Intent

Provide a sandboxed Starlark script runner tool (`code_mode`) that lets models execute multi-step tool workflows, concurrency fan-outs, and intermediate data filtering inside a single tool invocation, returning only the script's output and final return value while enforcing strict sandboxing, keyword-argument tool bindings, step/time/call/output limits, and full audit and interceptor parity through the nested execution pipeline.

## Goals

1. A single `codemode.New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)` constructor creates a code-mode tool that declares the given tools as its `ReachableTools` and generates a non-drifting, typed tool description from their `InputSchema` and `OutputSchema`.
2. The constructor calculates and reports the byte and character size of the generated description via `BuildInfo` so callers can evaluate context consumption before dispatching tool sets.
3. Every bound tool is callable within a Starlark script as a keyword-argument function returning its structured `Data` (or MCP `structuredContent`) on success and a distinct `ToolError` value on failure (including policy rejections), with zero exception throwing.
4. Independent tool calls can be dispatched concurrently inside a script via a `parallel(calls)` helper, bounded by `Options.MaxConcurrentCalls`, completing in the duration of the slowest call rather than the sum.
5. The Starlark runtime is strictly sandboxed: no filesystem, network, environment variable, process, or clock access is accessible from a script, and dynamic module loading (`load(...)`) is refused.
6. Execution enforces four configurable limits with safe defaults: wall-clock timeout (`MaxTimeout`), execution steps (`MaxSteps`), nested tool call count (`MaxCalls`), and output size (`MaxOutputBytes`).
7. Exceeding any limit halts execution cleanly, returning an error result naming the limit, preserving partial output, and listing all completed nested calls and their statuses.
8. Output exceeding `MaxOutputBytes` undergoes middle truncation (`TruncateMiddle`), preserving head and tail with an elided byte marker, and spills complete output to a disk file recorded in `core.ToolMetadata.SpillPath`.
9. An offline example (`examples/codemode/main.go`) demonstrates code mode running over built-in navigation tools and an MCP pool without network access or API keys using `provider/faux`.
10. Architectural adoption of `go.starlark.net` is recorded as a dependency ruling in `docs/DEPS.md`, and documentation across `docs/architecture.md`, `docs/configuration.md`, and `README.md` is updated.

## Non-goals

- **No host system access or general sandbox.** The script environment provides no access to OS files, network sockets, environment variables, system clocks, or child processes outside the tools explicitly bound to it.
- **No exception or unwinding mechanism in scripts.** Starlark scripts do not have `try`/`except`. Tool failures return values representing errors; script-level runtime panics or syntax errors terminate the script call.
- **No cross-script state persistence or resumption.** Mid-script state cannot be checkpointed or resumed across turns; each script runs to completion, limit, or failure within its single tool invocation.
- **No recursive nesting of code mode.** A code-mode tool cannot bind another code-mode tool directly or transitively.
- **No dynamic or progressive tool discovery functions.** All bound tools must be declared at construction time; scripts cannot search or query for unbound tools at runtime.
- **No changes to loop turn rules or making code mode mandatory.** Code mode is an ordinary `core.Tool` that embedders can register, omit, or mix with direct tools through `core.ToolPolicy`.
- **Earlier scopes of this split:**
  - `06_tool_output_schemas`: Output schema declarations on `core.Tool` and built-in tools, MCP schema pass-through, and conformance testing.
  - `07_nested_tool_calls`: Reachability declarations on `core.Tool`, cycle detection, policy resolution through wrappers, transitive unguarded shell checks, and nested execution dispatch via `core.CallNested`.

## Background

Multi-step agent tasks often require chained tool calls where intermediate results (such as large file reads or directory listings) consume substantial context window space. Code Mode patterns (such as Cloudflare Code Mode and Pi's `codemode` tool) expose tools as typed functions within a sandboxed runtime, letting the model inspect, filter, and aggregate results in script code before returning only the essential answer to the conversation context.

What exists today:

- `06_tool_output_schemas` established `OutputSchema *schema.Schema` on `core.Tool`, provided output schemas for all built-in tools (`read_file`, `write_file`, `edit_file`, `list_files`, `find_files`, `search_files`, `file_outline`, `find_symbol`, `find_references`, `fetch_url`, `execute`, `run_command`, `powershell`, `code_search`, and `subagent`), and implemented structured content pass-through for MCP tools.
- `07_nested_tool_calls` established `ReachableTools []Tool` and `Terminating bool` on `core.Tool`, cycle detection, recursive tool policy resolution, transitive unguarded shell checks (`core.ErrUnguardedExecute`), and the nested dispatch pipeline (`core.CallNested(ctx, calls ...ToolUseBlock)`), interceptor isolation with `ParentToolUseID`, and concurrent parallel execution.
- `tools.Accumulator` (`tools/accumulator.go`) implements bounded output buffering, `TruncateMiddle` head-and-tail retention, elided byte markers, and spill file creation under a configurable directory.
- `core.ToolMetadata` (`core/tool.go`) carries `Truncated`, `TruncatedBy`, `TotalBytes`, `SpillPath`, and `DurationMS`.
- `provider/faux` (`provider/faux/faux.go`) provides scripted mock turns for offline testing of agents and tools without network or API keys.

## Requirements

### 1. Code Mode Tool Construction and Reachability

- The `codemode` package provides a constructor:
  ```go
  func New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error)
  ```
- `Options` supports:
  - `Name string`: Tool name. Defaults to `"code_mode"`.
  - `Description string`: Explicit description override. If empty, the dynamic description is generated.
  - `DescriptionTemplate string`: Go `text/template` template string replacing the default instructions template.
  - `Guidelines []string`: Custom guidelines for `core.Tool.PromptGuidelines`. Defaults to built-in code-mode guidance.
  - `MaxTimeout time.Duration`: Maximum script wall time. Default: 30s.
  - `MaxSteps uint64`: Maximum Starlark execution steps. Default: 100,000 steps.
  - `MaxCalls int`: Maximum total nested calls per script. Default: 50.
  - `MaxConcurrentCalls int`: Maximum concurrent calls in flight at once. Default: 8.
  - `MaxOutputBytes int`: Maximum output byte limit. Default: 102,400 bytes (100 KB).
  - `SpillDir string`: Directory for output spill files. Default: workspace spill dir or OS temp dir.
  - `DisableSpill bool`: When true, disables writing spill files on truncation.
- `BuildInfo` reports:
  - `DescriptionBytes int`: Length of the generated description in bytes.
  - `DescriptionChars int`: Length of the generated description in unicode characters.
  - `BoundToolsCount int`: Number of tools bound to the code-mode tool.
- Validation:
  - If `opts.Name == ""` it defaults to `"code_mode"`.
  - If any tool in `tools` has `t.Name == opts.Name` or is already a code-mode tool, `New` returns an error refusing self-nesting (`"codemode: cannot bind code_mode tool inside code_mode"`).
  - If duplicate tool names exist in `tools`, `New` returns an error (`"codemode: duplicate tool name: <name>"`).
- The returned `core.Tool`:
  - Sets `Name: opts.Name`.
  - Sets `ReachableTools: tools`.
  - Sets `InputSchema`: an object schema requiring a string parameter `"script"` ("The Starlark script to execute.").
  - Sets `OutputSchema`: an object schema describing the structured return object (`output` string, `return_value` untyped/any, `calls_completed` array of call summary objects).
  - Sets `Execute`: handler dispatching to the script runner.

### 2. Dynamic Description and Schema Generation

- Unless `opts.Description` is set, `New` renders the tool description dynamically from the runtime instructions template and the schemas of `tools`.
- Instructions section documents:
  - Runtime language: Starlark (Python 3 dialect without exceptions or module imports).
  - Sandboxed execution: no access to filesystem, network, environment, processes, or clock except through bound tools.
  - Calling tools: bound tools are functions taking keyword arguments only; positional arguments are rejected.
  - Error handling: functions return structured values on success and `ToolError` on failure; check via `is_error(res)`, `res.ok`, or `res.is_error`.
  - Concurrency: execute groups of calls concurrently with `parallel([call(tool, **kwargs), ...])`.
  - Return values and output: print text using `print(...)` or return a value from `def main():` or module variable `result`.
  - Side effects: completed tool actions are permanent and not undone if later script steps fail.
- Bound tools section generates a typed Starlark function signature and docstring for each tool:
  - Function name matching `tool.Name`.
  - Parameter list derived from `tool.InputSchema`: names, Starlark types (`str`, `int`, `float`, `bool`, `list`, `dict`), required vs default values (`= None`), and descriptions.
  - Return type derived from `tool.OutputSchema`: field names and types. If `OutputSchema` is nil, return type is documented as untyped `Any`.
  - Tool description from `tool.Description`.
- The instructions template is defined as a prompt document viewable and replaceable via `opts.DescriptionTemplate`.

### 3. Sandboxed Starlark Environment

- Each execution creates a fresh `starlark.Thread` (`go.starlark.net/starlark`).
- Sandbox constraints:
  - `thread.Load` is configured to reject any module import (`"load is not permitted in code_mode scripts"`).
  - Module globals contain only:
    - Standard safe Starlark builtins (`len`, `range`, `str`, `int`, `float`, `bool`, `list`, `dict`, `min`, `max`, `sorted`, `enumerate`, `zip`, `print`, `type`, `repr`, `abs`, `hasattr`, `getattr`, `dir`).
    - Bound tool functions.
    - Concurrency helpers: `parallel`, `call`.
    - Error helper: `is_error`.
  - No access to OS filesystem (`open`, `os`), network (`socket`, `http`), environment (`os.environ`), subprocesses, or time/clock functions.

### 4. Keyword Argument Tool Bindings and Error Values

- Each bound tool is exposed as a Starlark `Builtin` callable function.
- Argument handling:
  - Positional arguments are rejected: if `len(args) > 0`, the function call fails with a Starlark error (`"<tool> only accepts keyword arguments"`).
  - Keyword arguments are decoded from Starlark values to Go JSON-compatible values (`string`, `int64`, `float64`, `bool`, `[]any`, `map[string]any`, `nil`) and marshaled into `json.RawMessage`.
- Invocation:
  - The function creates a `core.ToolUseBlock` with a generated ID and invokes `core.CallNested(ctx, block)`.
  - If `core.CallNested` returns `core.ErrTerminated`, the script runner immediately terminates execution and propagates `ToolResult.Terminate = true`.
  - If `core.CallNested` returns context cancellation/timeout, script execution halts with an abortion error.
- Return values:
  - If the call succeeds (`res.OK == true`):
    - If `res.Data` is non-nil, converts `res.Data` into Starlark values (`*starlark.Dict`, `*starlark.List`, etc.) and returns it.
    - If `res.Data` is nil, returns a Starlark dict containing `{"text": res.Text}`.
  - If the call fails (`res.OK == false`):
    - Returns a `ToolError` Starlark value (`type(v) == "tool_error"`).
    - `ToolError` has attributes: `ok = False`, `is_error = True`, `error` (error code string), `detail` (error detail string), `data` (structured error payload if any).
    - `ToolError` supports dictionary-style key indexing (`err["error"]`, `err["detail"]`, `err["ok"]`).
    - Global `is_error(val)` returns `True` if `val` is a `ToolError`, `False` otherwise.
    - No Starlark exception is raised; the script continues executing.

### 5. Concurrent Call Helper (`parallel`)

- The Starlark global environment exposes:
  - `call(tool_fn, **kwargs)`: returns a call descriptor holding the target tool and keyword arguments.
  - `parallel(calls)`: executes multiple tool calls concurrently.
- `parallel` accepts a Starlark list or tuple where each element is:
  - A call descriptor created by `call(tool_fn, **kwargs)`.
  - A 2-tuple or 2-element list `(tool_fn, kwargs_dict)` or `("tool_name", kwargs_dict)`.
  - A dict `{"tool": tool_fn, "args": kwargs_dict}`.
- Execution:
  - Validates all calls against the bound tools and remaining `MaxCalls` budget.
  - Divides the calls into concurrent batches of at most `opts.MaxConcurrentCalls`.
  - For each batch, creates `[]core.ToolUseBlock` and calls `core.CallNested(ctx, blocks...)` concurrently.
  - Maps each result to its Starlark value (structured dict or `ToolError`).
  - Returns a Starlark `*starlark.List` with results in the identical order of the input `calls`.
- If any call within `parallel` encounters an interceptor terminate vote (`core.ErrTerminated`), execution halts immediately and sets `Terminate = true`.

### 6. Resource Limits and Budget Enforcement

- Script execution enforces four limits:
  1. `MaxTimeout`: enforced using a context with timeout wrapping `ctx`. On timeout, halts with error code `"timeout"`.
  2. `MaxSteps`: configured on the thread via `thread.SetMaxExecutionSteps(opts.MaxSteps)`. On step exhaustion, halts with error code `"step_limit_exceeded"`.
  3. `MaxCalls`: counts total nested calls dispatched across single calls and `parallel`. Attempting to exceed `opts.MaxCalls` halts with error code `"call_limit_exceeded"`.
  4. `MaxOutputBytes`: limits cumulative printed and returned output bytes. On limit breach, halts with error code `"output_limit_exceeded"`.
- When any limit is exceeded:
  - The script runner returns `core.ToolResult{OK: false, Error: <limit_code>}`.
  - `Detail` states which limit was reached and the limit value.
  - `Text` displays the error, the limit breached, the partial output captured up to that point, and the list of completed nested calls.
  - `Data` contains `error`, `message`, `partial_output`, and `calls_completed`.

### 7. Output Capture, Middle Truncation, and File Spilling

- `thread.Print` captures all `print(...)` output into an internal `tools.Accumulator` configured with `tools.TruncateMiddle`, byte capacity `opts.MaxOutputBytes`, and `opts.SpillDir`.
- Script completion captures:
  - Printed output from the accumulator.
  - Return value: if `def main():` is defined in module globals, executes `main()` and records its return value; otherwise, if global variable `result` is defined, records `result`; otherwise records `None`.
- Rendering `ToolResult.Text`:
  - If output was printed and return value is not `None`: combines printed text and formatted return value.
  - If only output was printed: returns printed text.
  - If only return value was provided: returns formatted return value.
  - If neither was provided: returns `"[Script finished with no output]"`.
- Truncation and spilling:
  - If total output exceeds `opts.MaxOutputBytes`:
    - The accumulator middle-truncates output, retaining head and tail separated by an elided byte marker `[<N> bytes elided of <total> total. Full output: <path>]`.
    - Complete output is streamed to a spill file in `opts.SpillDir` (unless `opts.DisableSpill` is true).
    - `ToolResult.Metadata` sets `Truncated: true`, `TruncatedBy: "bytes"`, `TotalBytes`, `SpillPath`, and `DurationMS`.

### 8. Script Error Handling and Telemetry

- Script failures (syntax errors, unknown variables, type errors, invalid arguments) return `core.ToolResult{OK: false}`:
  - `Error`: specific error code (`"syntax_error"`, `"runtime_error"`, `"invalid_arguments"`, or `"script_failed"`).
  - `Detail`: Starlark error message including file/line information.
  - `Text`: formatted error message including line number, error detail, partial output, and completed calls.
  - `Data`: structured object containing `error`, `message`, `line`, `partial_output`, and `calls_completed`.
- Completed call tracking:
  - Each nested call executed records `{tool: name, arguments: args, ok: bool, error: err_code}` in an execution ledger.
  - On failure or limit termination, the ledger is included in `ToolResult.Data["calls_completed"]` and formatted in `ToolResult.Text`.
  - Tool side effects are not rolled back.
- Cancellation:
  - If caller `ctx` is cancelled, script execution halts cleanly, returning `core.ToolResult{OK: false, Error: "aborted", Detail: "Operation aborted"}`.

### 9. Offline End-to-End Example

- Provides an example application in `examples/codemode/main.go` with accompanying `examples/codemode/README.md`.
- The example constructs a code-mode tool binding built-in navigation tools (`read_file`, `list_files`, `find_files`) and an in-memory MCP tool.
- The example runs against `provider/faux` with canned scripted model turns that write Starlark scripts to inspect files and run parallel queries.
- Runs completely offline without API keys or external network connections (`go run ./examples/codemode`).

### 10. Documentation and Dependency Governance

- Creates `docs/DEPS.md` recording the dependency ruling for `go.starlark.net` (sandboxed deterministic execution, pure Go, zero host I/O, step counters).
- Updates `docs/architecture.md` to describe the code mode component and data flow.
- Updates `docs/configuration.md` to document `codemode.Options` and its defaults.
- Updates `README.md` to introduce code mode and link to the example.

## Design Decisions

1. **`codemode` package is a standalone top-level package.** Placing it at `github.com/agentfox/agentkit-go/codemode` mirrors established sibling packages like `codesearch`, `subagent`, and `prompt` without adding script dependencies to the root `core` package.
2. **`New` returns `(core.Tool, BuildInfo, error)`.** Returning `BuildInfo` alongside the tool gives embedders exact prompt cost metadata (byte and character count) before submitting tool sets to an LLM provider, while returning an error validates configuration and cycle invariants upfront.
3. **Starlark execution engine on `go.starlark.net`.** Starlark is pure Go, provides deterministic execution without thread races, has step-count budget enforcement built into `starlark.Thread`, and has zero host I/O capabilities unless explicitly provided by host functions.
4. **Keyword-argument only tool functions.** Tool schemas are key-value objects without positional guarantees; requiring keyword arguments prevents parameter misalignment and ensures scripts map cleanly to tool input schemas.
5. **Exception-free `ToolError` return values.** Starlark does not feature `try`/`except` control flow; returning a structured error value allows scripts to inspect error codes and details using familiar conditional checks (`is_error(res)` or `res.ok`) without uncatchable panics.
6. **Unified `parallel` helper supporting descriptors and tuples.** Supporting both `call(tool, **kwargs)` and `(tool, kwargs)` syntax accommodates natural LLM generation patterns while consolidating dispatch into a single `core.CallNested` batch.
7. **Concurrency capped by `MaxConcurrentCalls` (default 8).** Chunking concurrent calls prevents resource exhaustion and goroutine flooding during large fan-outs while maintaining parallel throughput.
8. **Script return value precedence (`main()` > `result` > `None`).** Checking for a callable `main()` first, then module variable `result`, and defaulting to `None` supports both structured procedural scripts and simple imperative scripts.
9. **Head-and-tail middle truncation with spill file support.** Reusing `tools.Accumulator` preserves both the beginning and conclusion of script outputs while offloading full output to disk, matching the behavior of shell tools.
10. **Pre-agent validation against nested code-mode tools.** Rejecting nested code-mode tools at constructor time (`codemode.New`) stops recursive script execution before agent construction and cycle checks.

## Dependencies

| Spec | Why this spec depends on or modifies it |
|---|---|
| `06_tool_output_schemas` | Consumes `OutputSchema` from `core.Tool` to dynamically generate typed return shapes in tool descriptions and map MCP `structuredContent`. |
| `07_nested_tool_calls` | Dispatches nested calls via `core.CallNested(ctx, calls...)`, relies on `ReachableTools` declarations, interceptor context propagation, and parallel nested execution. |
| `04_runner_and_tool_metadata` | Uses `core.ToolMetadata` truncation fields (`Truncated`, `SpillPath`, `TotalBytes`, `DurationMS`) and `tools.Accumulator` middle truncation. |

## Verified External API

| Package | Symbol | Real Signature / Implied Shape | Status |
|---|---|---|---|
| `go.starlark.net/starlark` | `ExecFile` | `func ExecFile(thread *Thread, filename string, src any, globals StringDict) (StringDict, error)` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `Call` | `func Call(thread *Thread, fn Value, args Tuple, kwargs []Tuple) (Value, error)` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `NewBuiltin` | `func NewBuiltin(name string, fn func(thread *Thread, b *Builtin, args Tuple, kwargs []Tuple) (Value, error)) *Builtin` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `Thread` | `type Thread struct { Print func(thread *Thread, msg string); Load func(thread *Thread, module string) (StringDict, error); ... }` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `(*Thread).SetMaxExecutionSteps` | `func (th *Thread) SetMaxExecutionSteps(steps uint64)` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `(*Thread).Cancel` | `func (th *Thread) Cancel(reason string)` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/starlark` | `Value` | `type Value interface { String() string; Type() string; Freeze(); Truth() Bool; Hash() (uint32, error) }` | unverified (external dependency to be added to `go.mod`) |
| `go.starlark.net/syntax` | `FileOptions` | `type FileOptions struct { Set bool; GlobalReassign bool; Recursion bool; ... }` | unverified (external dependency to be added to `go.mod`) |
