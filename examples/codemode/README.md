# codemode — one tool call that runs a script over many tools

Code mode gives the model a single tool, `code_mode`, that runs a Starlark
script. Inside the script every bound tool is a function, so the model can
chain calls, run independent ones concurrently with `parallel(...)`, and
filter large results down before anything reaches the conversation. Only what
the script prints and returns comes back.

The script is sandboxed. It has no file system, network, environment,
processes or clock, only the bound tools, and every call it makes goes through
the agent's own interceptor and event pipeline.

## Run it

No key and no network: the model is `provider/faux` replaying two scripted
`code_mode` calls.

```bash
go run ./examples/codemode
```

## What you'll see

- The size of the generated tool description (`BuildInfo`): it declares each
  bound tool as a typed Starlark function.
- One line per nested call, read from the agent's event stream, each naming
  the `code_mode` call that made it:
  `nested: call_2 called inventory__stock (ok=true)`.
- The first script lists the workspace, keeps only the `.go` files, and reads
  a file that does not exist. That failure comes back as a value
  (`is_error(missing)`, `missing.error` is `read_failed`) and the script
  carries on.
- The second script reads the notes concurrently, asks an MCP server's tool
  about two items at once, and returns a dict:
  `Return value: {"apples": 12, "pears": 0}`.
- The run ends with `Code mode example completed successfully`.

The transcript holds the two `code_mode` calls and their results, not the
seven calls the scripts made. Those are on the event stream.

## Where to look

- `core.Named(all, "list_files", "find_files", "read_file")` picks, from
  `tools.For`'s result, the three built-in tools the scripts may call, in that
  order. A name the set does not hold is an error that lists the names it
  does.
- `faux.ToolCall(id, name, args)` and `faux.Text(s)` script the model: two
  `code_mode` calls, then the final answer. Each is one whole turn.
- `agentkit.New(agentkit.Config{Provider: model, Model: faux.Model().ID,
  Toolset: agentkit.Toolset{Tools: []core.Tool{cm}}, MaxTurns: 5})` gives the agent `code_mode` alone;
  the tools it binds are its `ReachableTools`, which the model never sees
  directly. None is a shell tool, so no `Toolset.Guard` is required.
- `codemode.New(tools, opts)` builds the tool. See `docs/configuration.md`
  for `codemode.Options` and its limits.
- `docs/architecture.md` ("Code mode") explains how a script's calls reach the
  tools.
