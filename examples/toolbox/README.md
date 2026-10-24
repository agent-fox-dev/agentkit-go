# toolbox: call the built-in tools by hand

An agent's tools are ordinary Go values. This example builds the same set an
agent gets from `tools.For` and calls them with no model, through
`agentkit.NewRunner`, the same path an agent's calls take. Use it to see what
each tool takes and returns, and to try your own arguments on your own code. It
needs no API key and no network.

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
| `tour` (the default) | Calls every tool on a sample project it creates and removes, because the tour writes files. It takes no `--dir` or `--allow`. |
| `list` | Prints each tool's description and parameters. |
| `call NAME [JSON \| KEY=VALUE…]` | Calls one tool in `--dir` and prints the result. |
| `repl` | Reads `NAME {json}` or `NAME key=value …` lines from standard input until `quit`. |

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Workspace root; the file tools cannot reach outside it. |
| `--allow` | none | Programs `execute` and `run_command` may start, comma-separated. With none, every shell call is `blocked_by_policy`. |
| `--data` | off | Also print each result's structured `Data` as JSON. |

Arguments are one JSON object, or `KEY=VALUE` pairs read as the type the tool's
schema gives that key: `max_results=5` is a number, `name=5` is the string
`"5"`, and an array is JSON, as in `argv='["ls","-l"]'`.

```
$ find_symbol {"kind":"type","name":"Runner"}
  1 symbols matching "Runner"  (index: 944 files; go/ast 799, none 144, tree-sitter 1)
  runner.go:51-59  type  type Runner struct
```

## What it shows

The tour makes 18 calls, each printed as `$ tool {json}` followed by what a
model would read:

- **Look around:** `list_files`, `find_files`.
- **Read and search:** `read_file` with an offset, `search_files` with context lines.
- **Navigate:** `file_outline`, `find_symbol`, `find_references`.
- **Change:** `write_file`, `edit_file`, and an `edit_file` refused because its
  text occurs twice.
- **Run:** `execute` through `guard.Allow("echo")`, the same tool with `rm`
  blocked, and `run_command`, which takes an argument vector and no shell.
- **Get it wrong:** a path outside the workspace, arguments the schema refuses,
  and an unknown tool. Each is a result with an error code, not a Go error.

It ends with the typed route: `tools.Search` and `tools.References` return Go
values without the tool envelope, and `find_symbol` and `find_references` keep
the same values in their result's `Data`.

## The code that matters

```go
ws, err := workspace.Open(dir, workspace.Options{})
built, err := tools.For(ws, tools.Options{})
runner, err := agentkit.NewRunner(agentkit.Toolset{Tools: built, Guard: guard.Allow("ls", "git")})

res := runner.Call(ctx, "find_symbol", json.RawMessage(`{"name":"Runner","kind":"type"}`))
if !res.OK {
	fmt.Println(res.Error, res.Detail) // a code and its explanation
}
fmt.Print(res.LLMText()) // what a model reads; res.Data has the same answer for a program
```

`Runner.Call` looks the tool up, checks the arguments against its schema, asks
the guard and runs the handler, and always returns a `core.ToolResult`, never a
Go error. A `Runner`, like an agent, refuses a tool set that reaches a shell
tool with no `Guard`.

## Further reading

- [Examples index](../README.md); [`indexedsearch`](../indexedsearch) for ranked, indexed search
- [User guide](../../docs/guide.md): [the workspace and the built-in tools](../../docs/guide.md#the-workspace-and-the-built-in-tools), [running tools without a model](../../docs/guide.md#running-tools-without-a-model)
- [Configuration: built-in tools](../../docs/configuration.md#built-in-tools-toolsoptions)
