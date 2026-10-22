# toolbox — call the built-in tools by hand

An agent's tools are ordinary Go values. This example builds the same set an
agent gets from `tools.For` and calls them with no model, so you can see what
each one takes and returns, and try your own arguments on your own code. It
needs **no API key and no network**.

## Run it

Flags go before the command.

```bash
go run ./examples/toolbox                                  # a tour of every tool
go run ./examples/toolbox list                             # the tools and their parameters
go run ./examples/toolbox --dir . call find_symbol name=Runner kind=type
go run ./examples/toolbox --dir . call find_references '{"name":"Runner.Call"}'
go run ./examples/toolbox --dir . --data call file_outline path=runner.go
go run ./examples/toolbox --dir . --allow ls,git call execute command='git status --short'
go run ./examples/toolbox --dir . repl                     # type calls, one per line
```

| Command | Does |
|---|---|
| `tour` (the default) | Calls every tool on a sample project it creates for the run and removes afterwards, because the tour writes files. It takes no `--dir`. |
| `list` | Prints each tool's description and parameters: type, required or optional, and what it means. |
| `call NAME [JSON \| KEY=VALUE…]` | Calls one tool in `--dir` and prints the result. |
| `repl` | Reads `NAME {json}` or `NAME key=value …` lines from standard input until `quit`. |

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Workspace root; the file tools cannot reach outside it. |
| `--allow` | none | Programs `execute` and `run_command` may start, comma-separated. With none, every shell call is `blocked_by_policy`. |
| `--data` | off | Also print each result's structured `Data` as JSON. |

Arguments are one JSON object, or `KEY=VALUE` pairs. A pair is read as the type
the tool's schema gives that key: `max_results=5` is a number, `name=5` is the
string `"5"`, and an array or object is JSON, as in `argv='["ls","-l"]'`. A
value with spaces is quoted for your shell (`command='ls examples'`), or in the
`repl` written in the JSON form.

## What you'll see

The tour makes 18 calls, each as `$ tool {json}` followed by what a model would
read:

- **Look around:** `list_files`, `find_files` (`**` crosses directories).
- **Read and search:** `read_file` with a 1-based `offset` and the marker that
  names the call to continue with, `search_files` for a regular expression with
  context lines.
- **Navigate:** `file_outline`, `find_symbol` (ranked, narrowed by `kind` and
  `exact`) and `find_references`, whose sites say `resolved` when the type
  checker confirmed them.
- **Change:** `write_file`, `edit_file`, and an `edit_file` refused because its
  text occurs twice.
- **Run:** `execute` through `guard.Allow("echo")`, the same call with `rm`
  blocked, and `run_command`, where `;` is a character and not a shell operator.
- **Get it wrong:** a path outside the workspace, arguments the schema refuses,
  and a tool that does not exist. Each is a result with an error code, not a Go
  error.

It ends with the typed route. `tools.Search` and `tools.References` return
`SearchResult` and `ReferenceResult` values without the tool envelope, a
`find_symbol` result carries `[]tools.SymbolMatch` in `Data["symbols"]`, a match
converts to the `outline.Decl` that `tools.References` takes, and
`find_references` keeps its `tools.ReferenceResult` in `Data["result"]`.

## Where to look

- `newRunner` in `main.go` is the whole setup: `tools.For(ws, tools.Options{})`,
  then `agentkit.NewRunner(agentkit.Toolset{Tools: built, Guard: …})`.
  `Runner.Call(ctx, name, json)` runs a call through the steps an agent's calls
  go through: lookup, argument preparation and the schema check, the `Guard`,
  the handler. It returns a `core.ToolResult` and never a Go error. A tool's
  own `Execute` runs the handler alone.
- A `core.ToolResult` has `OK`, `Error` and `Detail` (a code and its
  explanation), `Text` (what the model reads), `Data` (the same answer for a
  program) and `Metadata` (which limit cut a long result). `show` prints them.
- The shell tools are in the set, and a `Runner` refuses a set that reaches one
  with no `Guard`. `guard.Allow(programs…)` is the shortest one that is safe by
  default; see `examples/codingagent` for `guard.Restricted` in an agent.
- To search by ranked, indexed queries instead (`sym:`, `file:`, `lang:`),
  see `examples/indexedsearch`.
- `docs/architecture.md` covers the tool set, the workspace and the runner.
