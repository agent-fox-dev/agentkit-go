# indexedsearch — the code-search index, end to end

`codesearch` is a nested module that adds one tool, `code_search`: ranked,
file-grouped answers from an in-process [zoekt](https://github.com/sourcegraph/zoekt)
trigram index, in zoekt's query language (`sym:`, `file:`, `lang:`, `case:`,
`or`, regular expressions). It also answers `find_symbol` from the outlines it
collected while building, and it keeps itself current as files change. This
example walks through all of it on a sample project it writes to a temporary
directory and removes. It needs **no API key and no network**.

## Run it

The example is its own module, because `codesearch` is one and its zoekt
dependency graph is kept out of the root module's.

```bash
cd examples/indexedsearch && go run .
```

## What you'll see

Ten sections, each call printed as `$ tool {json}` followed by what a model
would read. The sample project has 67 files the index can search, enough that a
few edits stay under its rebuild threshold, and four it cannot.

1. **Opt in.** `codesearch.New(ws, codesearch.Options{})` builds nothing, then
   `tools.For(ws, tools.Options{Index: idx})` puts `code_search` after the
   built-in tools. `find_symbol` is asked first, before any build, and answers
   from its own symbol table: `idx.Symbols` reports `ok=false` because it never
   starts a build.
2. **The first `code_search` builds the index.** The result's header counts the
   files matched and indexed. Four files of the sample cannot be searched, one
   for each reason the index reports in `skipped`: a NUL byte (`binary`), under
   three bytes (`too_small`), over 1 MiB (`oversized`) and more distinct
   trigrams than zoekt keeps per file (`too_many_trigrams`, a minified
   bundle). `idx.Build(ctx)` does the same ahead of time.
3. **The query language.** A lower-case word matches any case and a capital
   makes it exact; `sym:` matches declared names, never a mention; `file:` takes
   a regular expression on the path and `-file:` excludes; `lang:` limits by
   language; `or`, `and` and parentheses combine; a regular expression works as
   it is. `Data["files"]` gives a program the same answer: path, score, match
   count, chunks of lines and the symbols declared on the matched lines.
4. **`path`, `max_files`, `context_lines`.** The workspace's ignore rules apply
   to the index: `build/` is in `.gitignore`, so its file is not found. When more
   files match than `max_files`, the marker names the call that works.
5. **A query that cannot run** (empty, unparsable, a path outside the
   workspace) is a result with an error code, as for every tool.
6. **`find_symbol`, now answered by the index.** Its header names the
   `codesearch index` instead of the symbol table.
7. **Edits.** `write_file` and `edit_file` tell the index, which searches the
   file from a small overlay shard without rebuilding. An edit made some other
   way is invisible until `idx.Invalidate(rel)` names the file;
   `idx.Invalidate("")` says anything may have changed, as after a `git
   checkout`, and the next search walks the tree. When more than 5% of the
   indexed files are dirty, the next search rebuilds instead.
8. **Bounds.** `Options.MaxFiles`, `MaxBytes` and `MaxBuildTime` stop a build; the
   index keeps what it has and reports `partial` with the limit that fired. The
   demo trips the first two; the third is a wall-clock bound. A partial index
   still searches, and it leaves `find_symbol` to its own table.
9. **In an agent.** The same tools go to `agentkit.New` in a `Toolset`; a model
   on `provider/faux` calls `code_search` once and answers from the result.
10. **`Close`** removes the shards and is idempotent; a search afterwards is
    `index_closed`.

The run ends with `Code search example completed successfully`.

## Where to look

- `codesearch.New` returns a `tools.Index`, the same on every platform. On
  windows/amd64, where zoekt cannot run, the error is `codesearch.ErrUnsupported`
  and the embedder carries on with `Options.Index` nil; this program prints that
  and exits 0. Code that wants the counters (`BuildCount`, `BuildStats`,
  `IndexedFiles`, `RunDir`, `OverlayBuildCount`, `RevalCount`) asserts the result
  to `*codesearch.Index`, which also compiles on Windows.
- An index serves one workspace and takes its ignore rules from it. Give the
  rules to `workspace.Open`, not to the index.
- The tools `core.Named` picks here are not shell tools, so the runner needs no
  `Guard`. Add `"code_search"` to whatever tool list your agent is given.
- zoekt reports each shard it writes or loads through the standard `log`
  package, on stderr. The program sends those lines nowhere for the run; an
  embedder decides where they go.
- The Python symbol in section 6 (`load_orders`) comes from a tree-sitter
  grammar, which needs a cgo build. Without cgo `find_symbol` finds no such
  symbol, and `lang:python` still searches the file's text.
- `codesearch/README.md` has the differences from `search_files` (ranked, not
  exhaustive; a 1 MiB limit per file) and the pinned zoekt version.
- To call the other tools by hand, see `examples/toolbox`.
