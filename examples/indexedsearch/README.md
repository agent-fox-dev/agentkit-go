# indexedsearch: ranked code search over a trigram index

The nested `codesearch` module adds one tool, `code_search`: ranked,
file-grouped answers from an in-process [zoekt](https://github.com/sourcegraph/zoekt)
trigram index, queried in zoekt's language (`sym:`, `file:`, `lang:`, `case:`,
`or`, regular expressions). On a large repository, where `search_files` returns
long grep output that is often truncated, the index puts the best files first;
it also answers `find_symbol`, and keeps itself current as files change. This
example walks through all of it
on a sample project it writes to a temporary directory, with no API key and no
network.

## Run it

The example is its own Go module, because `codesearch` is one.

```bash
cd examples/indexedsearch && go run .
```

The output has ten sections; each call is printed as `$ tool {json}` followed
by what a model would read:

```
## 2. The first code_search builds the index (idx.Build does it ahead of time)
$ code_search {"query":"Checkout"}
  [2 files matched; 67 files indexed; index 44.2 KB; symbols: go/ast: 64, none: 3, tree-sitter: 2]
  checkout/checkout.go (2 matches) {Checkout}
    24: // Checkout charges the cart's total once. A payment that times out is
    26: func Checkout(c *cart.Cart, p Payer) (Receipt, error) {
  ...
builds 1; indexed 67 files; skipped: 1 binary, 1 too small, 1 over 1 MiB, 1 with too many trigrams
...
Code search example completed successfully
```

## What it shows

1. **Opting in.** `codesearch.New` builds nothing; `tools.Options{Index: idx}`
   adds `code_search` to the built-in tools.
2. **The first search builds the index**, and reports the files it skipped
   (binary, too small, over 1 MiB, too many trigrams). `idx.Build` does it
   ahead of time.
3. **The query language**: case rules, `sym:`, `file:` and `-file:`, `lang:`,
   `or` and parentheses, regular expressions. `Data["files"]` gives a program
   the same answer.
4. **`path`, `max_files`, `context_lines`**, and the workspace's ignore rules,
   which the index follows.
5. **A query that cannot run** is a result with an error code.
6. **`find_symbol` answered by the index** once it is built.
7. **Edits**: `write_file` and `edit_file` tell the index; an edit made another
   way needs `idx.Invalidate(path)`, or `idx.Invalidate("")` after something
   like a `git checkout`.
8. **Bounds**: `MaxFiles`, `MaxBytes` and `MaxBuildTime` stop a build, and the
   index reports itself as partial.
9. **In an agent**: the same tools go to `agentkit.New`; a scripted model calls
   `code_search` once.
10. **`Close`** removes the index files; a search afterwards is `index_closed`.

## The code that matters

```go
ws, err := workspace.Open(dir, workspace.Options{})
idx, err := codesearch.New(ws, codesearch.Options{})
switch {
case errors.Is(err, codesearch.ErrUnsupported):
	// windows/amd64: carry on with tools.Options{} and no index
case err != nil:
	return err
}
defer idx.Close()
built, err := tools.For(ws, tools.Options{Index: idx}) // code_search comes last
```

Things to know:

- An index serves one workspace and takes its ignore rules from it: give the
  rules to `workspace.Open`, not to the index.
- zoekt logs each shard it writes through the standard `log` package; the
  example discards those lines, and an embedder decides where they go.
- The Python symbol the example finds (`load_orders`) comes from a tree-sitter
  grammar, which needs a cgo build; without cgo, `lang:python` still searches
  the file's text.

## Further reading

- [Examples index](../README.md); [`toolbox`](../toolbox) calls the other built-in tools by hand
- [User guide: indexed code search](../../docs/guide.md#indexed-code-search)
- [Configuration: `code_search`](../../docs/configuration.md#code_search-when-a-codesearch-index-is-set)
- [`codesearch/README.md`](../../codesearch/README.md): differences from `search_files` and the pinned zoekt version
