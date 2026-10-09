# Command-line programs

AgentKit is a library and ships no command of its own. The programs under
`examples/` are runnable; their flags are listed here for reference. They use
the standard `flag` package, so `-flag` and `--flag` are equivalent.

## Make targets

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint`, `test` — run before committing |
| `make test` | `go test ./...` in the root and `codesearch` modules |
| `make vet` | `go vet` in the root and `codesearch` modules |
| `make lint` | `golangci-lint` in the root and `codesearch` modules (skipped if not installed) |
| `make fmt` | `gofmt -l -w .` |
| `make tidy` | `go mod tidy` in the root and `codesearch` modules |

## Examples

Every example that calls a model honours `AGENTKIT_MODEL` and needs an
Anthropic credential ([`configuration.md`](configuration.md)).

| Program | Flags |
|---|---|
| `examples/codingagent` | `--dir` (workspace root, default `.`); the task is the positional arguments |
| `examples/mcp` | `--external CMD` (also connect to a real MCP server spawned from this command line); the prompt is the positional arguments |
| `examples/customtools` | none; the prompt is the positional arguments |
| `examples/agentdemo`, `examples/codemode` | none; no key or network needed |
