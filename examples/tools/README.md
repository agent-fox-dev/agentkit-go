# Built-in tools, one program each

Every built-in tool an agent can hand a model has a program here, named after
the tool as the model sees it. Each one takes the tool's JSON arguments exactly
as the model would send them, makes the call the way the agent loop makes it,
and prints what the model would read back. No model, no API key.

```bash
go run ./examples/tools/read_file '{"path":"core/result.go","limit":8}'
go run ./examples/tools/search_files -schema
echo '{"pattern":"**/*_test.go"}' | go run ./examples/tools/find_files -dir ./tools
```

## Where the tools come from

`tools.All(tools.Options{Workspace: ws})` returns the default set — the file
tools, the navigation tools and the three shell tools. Every file path is
resolved against the workspace root, symlinks included, and a path outside it
is refused (`path_not_allowed`).

`fetch_url` is **not** in that set. A tool that makes outbound requests on the
model's behalf is a different risk class, so an agent opts in with
`tools.FetchTool(tools.FetchOptions{})` through `ToolPolicy.CustomTools` (and
names it in `ToolPolicy.ToolNames` if it uses an allowlist). Running the
`fetch_url` program is that opt-in.

`code_search` is not here either: its index imports zoekt, so it lives in the
nested module [`examples/codesearch`](../codesearch), and its program is
[`examples/codesearch/code_search`](../codesearch/code_search). An agent gets
it by passing a `codesearch.New` index as `tools.Options.Index`, which appends
`code_search` to `tools.All`'s set — and the program builds it exactly that
way.

## The command line

```
go run ./examples/tools/<tool> [flags] ['<json arguments>']
```

The arguments are one positional argument (quote it) or, when absent, all of
stdin. An empty input is `{}`.

| Flag | Meaning |
|---|---|
| `-dir DIR` | Workspace root (default `.`). The file tools cannot reach outside it. |
| `-schema` | Print the tool's name, description and JSON input schema as they go in the request's tool list, and its prompt guidelines as the system prompt adds them; then exit. |
| `-data` | Also print the structured result — `ok`, `data`, `error`, `detail`, `metadata` — as JSON on stderr. |
| `-allow PROG` | One more program the shell guard admits. Repeatable, or comma-separated. |
| `-allow-all` | No guard at all (`guard.AllowAll`): an unrestricted shell. |

**Stdout is exactly the text block the model reads** (followed by a newline if
it does not end in one), so it can be piped. Everything else goes to stderr:
`[is_error: <code>]` for an error result, a line per image block (`read_file`
on an image returns a note plus the image), and the `-data` dump.

Exit status: `0` the result is OK, `1` the result is an error — what the model
receives with `is_error` set — and `2` when no call was made: a bad flag, more
than one positional argument, arguments that are not a JSON object, a `-dir`
that does not exist, or a tool set that could not be built (`code_search` on
Windows).

Ctrl-C cancels the call's context, as an abort does inside an agent; the shell
tools kill the process tree.

## What "the same way the model would" means

The shared plumbing is [`toolcli`](toolcli/toolcli.go); every `main.go` is
`toolcli.Main("<tool>")`. A call goes through the steps the loop
(`batch.go`) puts it through, using the same exported functions:

1. **Parse** — `core.NewToolUse` turns the bytes into the tool_use the model
   sent, key order kept.
2. **Prepare** — `core.PrepareArguments`: per-tool repair, optional nulls
   dropped, primitives coerced against the schema (`"limit":"5"` becomes `5`),
   then validation. A failure is an `invalid_arguments` result that echoes the
   arguments back, as the model would see it.
3. **Authorize** — the `BeforeToolCall` interceptor (see below). A block is a
   `blocked_by_policy` result with the guard's reason.
4. **Execute** — the tool's own `Execute(ctx, json.RawMessage)`.
5. **Render** — `core.ToolResult.LLMText()`, the function the loop's
   `toolResultMessage` uses for the tool_result text block: the tool's own
   `Text` rendering when it has one (`read_file`, `search_files`, the shell
   tools, …), otherwise the JSON envelope `{"ok":…,"data":…}` with metadata
   stripped.

On the Anthropic and Google wires the error flag travels beside the text; the
OpenAI wires have no such flag, so `provider.ToolResultText` prefixes an error
result's text with `Error: `. The program prints the text block and reports
the flag on stderr and in the exit status.

Not reproduced: plugin hooks, `AfterToolCall` and the audit trail, which an
embedder adds around the call and which are empty unless configured.

## The shell guard

An agent cannot register `execute`, `run_command` or `powershell` without a
`BeforeToolCall` interceptor — the run fails with `core.ErrUnguardedExecute`.
So the programs apply one by default, the same one
[`codingagent`](../codingagent) uses: `guard.Restricted` admitting `go`, `git`,
`ls`, `cat` and `rg`, with shell operators (`|`, `;`, `&&`, redirection,
substitution) and `NAME=value` prefixes refused. `-allow` widens the program
list; `-allow-all` swaps in `guard.AllowAll`, the explicit "unrestricted
shell". `guard.Restricted` has no PowerShell grammar filter, so it refuses
every `powershell` call: that program needs `-allow-all`.

The guard sees every tool call, not just shell ones; `guard.Restricted` only
judges the shell tools and lets the others through.

## The tools

Write tools change files: point `-dir` at a scratch directory when trying them.

| Tool | Purpose | Example |
|---|---|---|
| `read_file` | Read a file: at most 2000 lines or 50 KB per call, paged with `offset`/`limit`; an image comes back as a note plus the image. | `go run ./examples/tools/read_file '{"path":"core/result.go","offset":1,"limit":20}'` |
| `write_file` | Create or replace a whole file. **Writes.** | `go run ./examples/tools/write_file -dir /tmp/scratch '{"path":"notes/hello.txt","content":"hello\nworld\n"}'` |
| `edit_file` | Exact-match replacements, each `old_string` unique in the original file. **Writes.** | `go run ./examples/tools/edit_file -dir /tmp/scratch '{"path":"notes/hello.txt","edits":[{"old_string":"world","new_string":"tools"}]}'` |
| `list_files` | One directory's entries, directories with a trailing `/`. Ignores `.gitignore`. | `go run ./examples/tools/list_files '{"path":"guard"}'` |
| `find_files` | Glob search for paths, honouring `.gitignore`. | `go run ./examples/tools/find_files '{"pattern":"tools/*fetch*.go"}'` |
| `search_files` | RE2 search over file contents, with context lines and a file glob. | `go run ./examples/tools/search_files '{"pattern":"func \\(r ToolResult\\) LLMText","file_glob":"**/*.go","context_lines":1}'` |
| `file_outline` | A file's declarations with line ranges. | `go run ./examples/tools/file_outline '{"path":"guard/guard.go"}'` |
| `find_symbol` | Where a name is declared, across the workspace. | `go run ./examples/tools/find_symbol '{"name":"Restricted","kind":"func","exact":true}'` |
| `find_references` | Who uses a declaration. | `go run ./examples/tools/find_references '{"name":"PrepareArguments","path":"core","max_results":5}'` |
| `execute` | A command line run by `bash -c` (else `sh -c`; never `$SHELL`). Guarded. | `go run ./examples/tools/execute '{"command":"git log --oneline -3"}'` |
| `run_command` | A program and an argument list, no shell re-parsing. Guarded. | `go run ./examples/tools/run_command '{"argv":["go","version"],"timeout_s":10}'` |
| `powershell` | A PowerShell command. Needs `pwsh` on PATH (or Windows PowerShell 5.1 on Windows) and `-allow-all`. | `go run ./examples/tools/powershell -allow-all '{"command":"Get-ChildItem -Name"}'` |
| `fetch_url` | An HTTPS request through the SSRF guard; `as_text` extracts readable text from HTML. Needs the network; opt-in in agents. | `go run ./examples/tools/fetch_url '{"url":"https://example.com","as_text":true}'` |
| `code_search` | Ranked zoekt queries over an index of the workspace (nested module; not on Windows). | `cd examples/codesearch && go run ./code_search -dir ../.. '{"query":"sym:NewWorkspace","max_files":3}'` |

Notes:

- **`file_outline`, `find_symbol`, `find_references`** read Go with `go/ast`
  in every build. Other languages (Python, TypeScript, Rust, Java, …) are
  parsed with tree-sitter, which needs cgo: built with `CGO_ENABLED=0` they
  report no declarations for those files. Each result's header names the
  backend it used.
- **The shell tools** run with a reduced environment (`tools.ReducedEnv`):
  credentials in your shell's environment are not passed on. Output past the
  limits is truncated for the model and spilled in full to a file under
  `$TMPDIR/agentkit-spill-*`, whose path appears in `-data`'s metadata; the
  SDK never deletes spill files.
- **`fetch_url`** permits only `https://`, refuses private and loopback
  addresses, follows at most 5 redirects and caps the body at 512 KB.
- **`code_search`** builds its index on the first query, then discards it on
  exit: each run pays the build. [`codesearch/search`](../codesearch/search)
  keeps one index for many queries.

## Testing

```bash
go test ./examples/tools/
cd examples/codesearch && go test ./code_search/
```

Offline, no key: each tool is called against a temporary workspace with
arguments a model might send. `fetch_url` is exercised up to its scheme check,
and `powershell` runs only when `pwsh` is installed.
