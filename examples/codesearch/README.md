# codesearch examples

Four programs that use the [`codesearch`](../../codesearch) index: three on
its own, with no agent and no API key, and one that hands it to an agent as the
`code_search` tool.

```bash
cd examples/codesearch
go run ./search --dir ../.. 'sym:NewWorkspace'    # no key
go run ./freshness                                # no key
go run ./code_search -dir ../.. '{"query":"sym:NewWorkspace"}'   # no key
go run ./agent --dir ../.. "Which types implement tools.Index?"
go test ./...                                     # no key
```

They are a **nested module**, unlike the single-file examples beside them.
`codesearch` imports [zoekt](https://github.com/sourcegraph/zoekt), and the root
module is held to the standard library (see [`docs/DEPS.md`](../../docs/DEPS.md));
a nested module is the only way to keep zoekt out of the root's graph. Your own
program that imports `codesearch` pays for zoekt the same way — and a program
that does not import it pays nothing. The `go.mod` here points both
agentkit-go modules at this checkout with `replace`.

zoekt does not build on Windows. There, `codesearch.New` returns
`codesearch.ErrUnsupported` and each example prints that and exits.

| Example | Run it | What it teaches |
|---|---|---|
| [`search`](search) | `go run ./search --dir ../.. 'retry file:\.go$ -file:_test'` | **The index without an agent.** `New` is free and `Build` is optional; one index answers many queries; calling the `code_search` tool's `Execute` directly is the whole query surface; errors are results with stable codes; `Symbols` as a declaration lookup; the structured `Data` an application consumes instead of the rendered text. |
| [`freshness`](freshness) | `go run ./freshness` | **Keeping an index correct while files change.** The index never watches the filesystem: a write it is not told about is invisible, `Invalidate(path)` serves the file from an overlay, `Invalidate("")` revalidates the whole tree, more than 5% dirty rebuilds, and a closed index answers `index_closed`. |
| [`code_search`](code_search) | `go run ./code_search -dir ../.. '{"query":"sym:NewWorkspace"}'` · `-schema` | **The tool as the model calls it.** Takes `code_search`'s JSON arguments and prints the text the model would read, built through `tools.Options.Index` exactly as an agent gets it. The codesearch counterpart of the per-tool programs in [`examples/tools`](../tools), sharing their flags. |
| [`agent`](agent) | `go run ./agent --dir ../.. "<question>"` · `--compare` | **The opt-in.** `codesearch.New`, `tools.Options.Index`, `"code_search"` in the allowlist; the `ErrUnsupported` fallback; closing after the run, not after `tools.All`. `--compare` asks again without the index and prints both runs' turns, tool calls and cost. |

## `search`

```bash
go run ./search --dir ../.. 'sym:NewWorkspace'
go run ./search --dir ../.. 'retry file:\.go$ -file:_test' 'lang:go "func New"'
go run ./search --dir ../.. --path tools --max-files 3 ClampLimit
go run ./search --dir ../.. --symbol Runner --kind type
go run ./search --dir ../.. --json 'sym:Build'
```

| Flag | Default | Meaning |
|---|---|---|
| `--dir` | `.` | Directory to index; nothing outside it is read |
| `--path` | | Restrict every query to this file or directory (relative to `--dir`) |
| `--max-files` | 10 | Files per query, capped at 25 |
| `--context` | 2 | Context lines around each match |
| `--symbol NAME` | | Look up a declaration instead of running a query |
| `--kind` | | With `--symbol`: only this kind (`func`, `method`, `type`, …) |
| `--exact` | false | With `--symbol`: exact name rather than prefix |
| `--json` | false | Print the result's structured `Data` rather than its `Text` |

Positional arguments are queries in zoekt syntax. The tool description lists
the forms a model is shown:

| Query | Finds |
|---|---|
| `sym:Runner` | declarations named `Runner`, ranked above mere mentions |
| `retry file:\.go$ -file:_test` | `retry` in Go files that are not tests |
| `lang:python "def load"` | the exact phrase, in Python files |
| `(compaction or summarize) case:no` | either word, case-insensitively |

The index is built once and every query after the first runs against it — the
timings on stderr show the difference. That is the reason to keep an `Index`
around rather than search per call.

zoekt reports shard builds through the standard library's global `log`
package. All four examples silence it with `log.SetOutput(io.Discard)`; that is
the application's decision, since the logger is the process's, not the index's.

## `freshness`

```
1. first query builds the index
   sym:Retry      -> pkg/retry.go
   builds=1 overlays=0 revalidations=0
2. retry.go rewritten, index not told
   sym:RetryN     -> no matches
3. Invalidate("pkg/retry.go")
   sym:RetryN     -> pkg/retry.go
   builds=1 overlays=1 revalidations=0
4. Symbols(RetryN) -> ok=true matches=1 pkg/retry.go:4
5. gen/backoff.go added, pkg/file00.go deleted, Invalidate("")
   sym:Backoff    -> gen/backoff.go
   sym:Helper00   -> no matches
   builds=1 overlays=2 revalidations=1
6. five more files rewritten and invalidated (over 5%)
   sym:Renamed    -> pkg/file01.go, pkg/file02.go, pkg/file03.go, pkg/file04.go, pkg/file05.go
   builds=2 overlays=2 revalidations=1
7. after Close
   sym:Retry      -> error index_closed
```

Step 2 is the one to remember. When the index sits behind `tools.All`, the
model's own writes are reported for you — `write_file` and `edit_file` call
`Invalidate(path)`, the shell tools call `Invalidate("")`. Anything else that
writes into the workspace — your application, a `git checkout`, a generator —
has to make the same call, or `code_search` and `find_symbol` keep answering
from the old content.

Step 5 rebuilds rather than re-reading two files because it runs milliseconds
after the build: inside a two-second window an unchanged mtime does not prove an
unchanged file, so the walk counts every file as changed. A session older than
that re-reads only what moved.

## `agent`

The difference from [`codingagent`](../codingagent) is these lines:

```go
idx, err := codesearch.New(ws, codesearch.Options{Ignore: opts.Ignore})
if err != nil { /* errors.Is(err, codesearch.ErrUnsupported): run without it */ }
defer idx.Close()
opts.Index = idx                                   // tools.All appends code_search
cfg.ToolPolicy.ToolNames = append(names, "code_search") // only if you use an allowlist
```

The agent is read-only — no `write_file`, `edit_file` or shell tool is in its
allowlist — so it needs no execute guard. If yours writes, nothing changes
here: with `Options.Index` set, the write tools invalidate the index for you.
