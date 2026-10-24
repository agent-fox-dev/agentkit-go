# codesearch

Indexed code search for AgentKit agents. It adds a `code_search` tool backed by
an in-process [zoekt](https://github.com/sourcegraph/zoekt) trigram index, so
an agent working on a large repository gets fast, ranked results instead of a
scan of every file for every query.

## Why use it

The built-in `search_files` tool runs a regular expression over every file in
the workspace. That is exact and needs no setup, but on repositories with tens
of thousands of files it is slow, and its capped results often miss what the
agent was looking for, so the agent searches again. `code_search` answers from
an index:

- **Ranked results**: the best matches come first, grouped by file.
- **A query language**: substrings, regular expressions, and the filters
  `sym:` (declarations), `file:`, `lang:` and `case:`, combined with `or`
  and negation.
- **Symbol lookup**: the index can also answer the built-in
  `find_symbol` tool.
- **Kept up to date**: edits made through the tools reach the index
  automatically.

It is a separate Go module, so applications that do not use it do not link
zoekt and its dependencies.

## Install

```bash
go get github.com/agent-fox-dev/agentkit-go/codesearch
```

## Use it

Build the index for a workspace and hand it to `tools.For`, which then adds
`code_search` after the built-in tools:

```go
ws, err := workspace.Open(".", workspace.Options{})
if err != nil {
	return err
}
opts := tools.Options{}
idx, err := codesearch.New(ws, codesearch.Options{})
switch {
case errors.Is(err, codesearch.ErrUnsupported):
	// Windows: zoekt does not build there; carry on without the index.
case err != nil:
	return err
default:
	defer idx.Close()
	opts.Index = idx
}
ts, err := tools.For(ws, opts)
```

The index uses the workspace's ignore rules, is built on the first query, and
is bounded by file count, total size and build time
([options](../docs/configuration.md#code_search-when-a-codesearch-index-is-set)).
If you restrict the agent's tools with a policy, add `"code_search"` to the
allowed names.

The index also works without an agent: call the `code_search` tool directly,
and call `Invalidate` when files change outside the tools.

## code_search or search_files?

| | `search_files` | `code_search` |
|---|---|---|
| Query | RE2 regular expression | zoekt query: substring, regex, `sym:`, `file:`, `lang:`, `case:` |
| Results | Every match up to the cap, in file order | A ranked subset, best first |
| Setup | None | An index build on first use |
| Files skipped | Binary files | Binary files, files over 1 MiB, and generated-looking files (lock files, minified bundles) |

Keep both: `search_files` for exhaustive answers on small trees, `code_search`
for finding things in large ones.

## Platforms

Linux and macOS. On Windows, `codesearch.New` returns `ErrUnsupported` and a
nil index, and the code above runs unchanged without code search.

## Further reading

- [`examples/indexedsearch`](../examples/indexedsearch) walks through the
  index on a sample project, from the first query to an agent using it.
- [User guide: indexed code search](../docs/guide.md#indexed-code-search).
- [`docs/codesearch.md`](../docs/codesearch.md): why the module exists, how it
  is built on zoekt, and the dependencies it brings in.
