# codemode: one tool call that runs a script over many tools

Code mode gives the model a single tool, `code_mode`, that runs a Starlark
script. Inside the script every bound tool is a function, so the model can
chain calls, run independent ones concurrently with `parallel(...)`, and filter
large results before anything reaches the conversation: only what the script
prints and returns comes back. The script has no file system, network,
environment, processes or clock of its own, only the bound tools, and every
call it makes goes through the agent's own guard and event stream.

This example runs offline with no API key: the model is `provider/faux`
replaying two scripted `code_mode` calls over real tools (three built-in file
tools on a temporary workspace, and a stock lookup from an in-process MCP
server).

## Run it

```bash
go run ./examples/codemode
```

```
code_mode binds 4 tools; its description is 2753 bytes (2753 characters)

  nested: call_1 called list_files (ok=true)
  nested: call_1 called read_file (ok=false)
  nested: call_2 called find_files (ok=true)
  ...
  nested: call_2 called inventory__stock (ok=true)

code_mode result:
  Go files: main.go, util.go
  CHANGELOG.md: read_failed

code_mode result:
  README.md -> # Example project
  docs/NOTES.md -> Notes: the scripts read these concurrently.
  Return value: {"apples": 12, "pears": 0}
...
Code mode example completed successfully
```

## What it shows

- The generated tool description declares each bound tool as a typed
  Starlark function (`BuildInfo` reports its size).
- A failed call comes back to the script as a value (`is_error(missing)`,
  `missing.error`), and the script carries on.
- `parallel([call(...), ...])` runs independent calls concurrently.
- Every nested call appears on the agent's event stream with the `code_mode`
  call as its parent, but the transcript holds only the two `code_mode` calls
  and their results, not the seven calls the scripts made.

## The code that matters

```go
all, err := tools.For(ws, tools.Options{})
bound, err := core.Named(all, "list_files", "find_files", "read_file")
bound = append(bound, mcpTools...) // tools from an mcp.Pool work the same way

cm, info, err := codemode.New(bound, codemode.Options{})
agent, err := agentkit.New(agentkit.Config{
	Client:   model,
	Model:    faux.Model().ID,
	Toolset:  agentkit.Toolset{Tools: []core.Tool{cm}}, // the model sees code_mode alone
	MaxTurns: 5,
})
```

`core.Named` picks tools by name, in that order, and fails with the list of
names it has when one is missing. None of the bound tools is a shell tool, so
no `Toolset.Guard` is required; bind `execute` and you must supply one.

## Further reading

- [Examples index](../README.md)
- [User guide: code mode](../../docs/guide.md#code-mode)
- [Configuration: `codemode.Options`](../../docs/configuration.md#code-mode-codemodeoptions) and its limits
- [Architecture: code mode](../../docs/architecture.md#code-mode): how a script's calls reach the tools
