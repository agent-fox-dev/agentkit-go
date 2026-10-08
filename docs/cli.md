# Command-line programs

The SDK ships one command and one harness; the `examples/` applications are
runnable programs whose flags are listed here for reference. All use the
standard `flag` package, so `-flag` and `--flag` are equivalent.

## `validate-plugins`

`cmd/validate-plugins` is the reference driver for `plugins.Validate`
(REQ-PLUGIN-10). It loads every configured `plugin.toml`, runs interface
conformance checks and the import lint, and reports without starting anything.

```
go run ./cmd/validate-plugins -path ./plugins [-path ./more] [-disable name]
```

| Flag | Meaning |
|---|---|
| `-path DIR` | Directory to search for `plugin.toml`. Repeatable; at least one is required. |
| `-disable NAME` | Plugin name to skip. Repeatable. |

Exit codes: `0` clean, `1` at least one error-severity violation, `2` usage (no
`-path`). The command builds an empty registry, so every manifest reports as
unregistered; a host with plugins linked in should call `plugins.Validate`
with its own registry behind its own flag.

## `difftest`

The differential harness lives in the nested module `difftest/` (run it from
there).

```
cd difftest && go run ./cmd/difftest [-scenarios DIR] [-ledger FILE]
```

| Flag | Default |
|---|---|
| `-scenarios` | `scenarios` — directory of scenario directories |
| `-ledger` | `known-divergences.json` — accepted-divergence ledger |

Exit codes: `0` clean, `1` a failure or a dark run (no reference to compare
against), `3` a stale ledger entry. See [`PROVIDERS.md`](PROVIDERS.md) for what
DARK means.

## Make targets

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint`, `test` — run before committing |
| `make test` | `go test ./...` in the root, `codesearch`, `difftest` and `examples/codesearch` modules |
| `make vet` | `go vet` in the root, `codesearch`, `difftest` and `examples/codesearch` modules |
| `make lint` | `golangci-lint` in the root, `codesearch`, `difftest` and `examples/codesearch` modules (skipped if not installed) |
| `make fmt` | `gofmt -l -w .` |
| `make tidy` | `go mod tidy` in the root, `codesearch`, `difftest`, `examples/codesearch` (and `flatline` when `../spec/golang` exists) |
| `make build-examples` | Installs `triage` and `cleaner` into `$GOBIN` |

## Examples

Every example that calls a model honours `AGENTKIT_MODEL` and needs the
credential for its vendor ([`configuration.md`](configuration.md)).

| Program | Flags |
|---|---|
| `examples/chat`, `streaming`, `delegation`, `interactive`, `customtools`, `plugins`, `skills` | no flags; where an example takes a prompt it is the positional arguments |
| `examples/codingagent` | `--dir` (workspace root, default `.`) |
| `examples/session` | `--session PATH` (log file), `--reset` (delete the log first) |
| `examples/mcp` | `--serve` (expose this program's tools as an MCP server on stdio), `--external CMD` (also connect to a real server spawned from this command line) |
| `examples/mcpserver` | `--config FILE` (TOML with `[mcp_server]`), `--transport stdio\|http`, `--port N`, `--api-key-env VAR`; flags override the file |
| `examples/agentdemo`, `examples/testing` | none; no key or network needed |

### `codesearch`

`cd examples/codesearch && go run ./<program> [flags]` — three programs for the
optional code-search index, in a nested module (see
[`examples/codesearch/README.md`](../examples/codesearch/README.md)). Not
available on Windows, where each prints `codesearch.ErrUnsupported` and exits 1.

| Program | Flags |
|---|---|
| `search` | `--dir` (directory to index, default `.`), `--path` (restrict every query), `--max-files N`, `--context N`, `--symbol NAME` with `--kind` and `--exact` (declaration lookup instead of a query), `--json` (structured result); positional arguments are zoekt queries. No key needed |
| `freshness` | none; no key or network needed |
| `agent` | `--dir` (workspace root, default `.`), `--compare` (also answer without the index and print both runs' cost); the question is the positional arguments |

### `triage`

`go run ./examples/triage "<bug report>"` — triages a report into a GitHub
issue. See [`examples/triage/README.md`](../examples/triage/README.md).

| Flag | Meaning |
|---|---|
| `--dir` | Workspace root (default `.`) |
| `--repo owner/repo` | Target repository (default: `origin` of `--dir`) |
| `--dry-run` | Print the rendered issue; change nothing on GitHub |
| `--overwrite` | Overwrite the input issue's body instead of creating a new issue |
| `--label a,b` | Labels for the created issue |
| `--out FILE` | Also write the rendered issue to a file |
| `--debug` | Stream the model's reasoning text to stderr |
| `--verbose` | Input details, tools, traces and cost summary |

### `cleaner`

`go run ./examples/cleaner https://github.com/{owner}/{repo}/issues/{n}` —
implements the fix. See [`examples/cleaner/README.md`](../examples/cleaner/README.md).

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Repository to work in |
| `--land` | `pr` | `pr`, `branch`, `merge` or `none` |
| `--dry-run` | false | No push, PR or comments (local branch and commit are still made) |
| `--model` | `$AGENTKIT_MODEL` | Model spec |
| `--max-turns` | 100 | Per-phase turn ceiling |
| `--budget` | 5.0 | Per-phase spend ceiling, dollars |
| `--verify CMD` | detected | Command that decides success |
| `--no-verify` | false | Skip verification (result reported as unverified) |
| `--verify-timeout` | 10m | Timeout for one verification run |
| `--issue-file FILE` | | Read the issue from JSON instead of GitHub; implies no GitHub writes |
| `--journal FILE` | | Append a JSONL record of every step |
| `--allow a,b` | | Extra programs the implementation phase may run |
| `--show-text` | false | Print the model's prose too |
| `--push-attempts` | 4 | Push retries |
| `--verbose` | false | Tool calls, timing and cost diagnostics |

### `flatline`

`cd examples/flatline && go run . [flags] <spec>` — implements a spec pack task
group by task group (nested module; needs a sibling checkout of
`agent-fox-dev/spec`). See [`examples/flatline/README.md`](../examples/flatline/README.md).

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Repository to work in |
| `--specs-dir` | `<dir>/.specs` | Where `NN_name` spec directories live |
| `--land` | `merge` | `merge` (squash into the current branch) or `branch` (keep it) |
| `--push` | false | Push what the run lands |
| `--final-branch` | `feature/{spec_id}_{spec_name}` | Branch name with `--land=branch` |
| `--model` | `$AGENTKIT_MODEL` | Model spec |
| `--max-turns` | 300 | Per-session turn ceiling |
| `--budget` | 20.0 | Per-session spend ceiling, dollars |
| `--max-cost` | 0 | Run spend ceiling; 0 means none |
| `--max-retries` | 2 | Retries per task group |
| `--session-timeout` | 45m | Wall-clock ceiling per session |
| `--check-timeout` | 10m | Timeout for one test command |
| `--verifier` | false | Run the informational verifier after the last group |
| `--no-verifier` | false | Compatibility no-op |
| `--assume-deps` | false | Treat cross-spec dependencies as implemented |
| `--journal FILE` | | Append a JSONL record of every step |
| `--allow a,b` | | Extra programs the coder's shell may run |
| `--show-text` | false | Print the model's prose too |
| `--push-attempts` | 4 | Push retries |
| `--verbose` | false | Diagnostics |
