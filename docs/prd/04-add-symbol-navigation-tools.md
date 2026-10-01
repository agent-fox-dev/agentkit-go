# Add symbol navigation tools

Status: **proposed**. Where this sits relative to agent-fox, and the order
the two repositories move in, is recorded in agent-fox
[ADR 07](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/adr/07-split-code-navigation-between-agentkit-and-agent-fox.md).

## 1. The problem

AgentKit's file tools operate on paths and text: `read_file` (2 000 lines
or 50 KB, with `offset`/`limit`), `list_files` (one directory), `find_files`
(a glob) and `search_files` (RE2, at most 100 matches, in walk order). Two
questions a coding agent asks constantly have no direct answer:

- **"What is in this file?"** The only answer is to read it. A 1 200-line
  file costs 1 200 lines of context to find the one function that mattered.
- **"Where is `Runner` defined?"** The answer is a regex such as
  `func \(?.*\)? ?Run\b|type Runner\b`. The model writes it, gets back
  definitions mixed with call sites in walk order, and reads files to tell
  them apart.

In agent-fox's `spec` runs, the turns go mostly to this: `search_files`, then
`read_file` of whole files, then another `search_files`. Each turn re-sends
the context, so the cost compounds.

## 2. Goals and non-goals

**Goals.**

1. A model can get a file's declarations, with line ranges, without reading
   the file, and then read only the range it needs with `read_file`'s
   existing `offset`/`limit`.
2. A model can find where a name is declared, across the workspace, ranked
   so that definitions come back and call sites do not.
3. An embedder can build its own views (agent-fox's repository map, agent-fox
   PRD 06) from the same data the tools use, through an exported package. No
   embedder has to write a second walk or a second `.gitignore` engine.
4. The root module stays standard-library-only and cgo-free (REQ-GO-11,
   NFR-COMPAT-06). Both `internal/policy` tests stay green with no new entry
   in `allowedModules`.
5. Results never disagree with the other file tools about which files
   exist. The outline and symbol tools walk through the same ignore engine,
   hidden-entry rule and `Workspace` confinement as `search_files`.

**Non-goals.**

- **References, callers, type resolution.** These need a type checker per
  language. "Who calls X" stays `search_files` with `\bX\b`. A language
  server client is a separate PRD.
- **Exact parity between backends.** `search_files` promises that its
  ripgrep and native paths return the same results. Symbol extraction cannot
  promise that: universal-ctags and a heuristic do not see the same
  declarations. Instead, every result names the backend that produced it
  (§4.4), so a disagreement can be diagnosed.
- **An index that persists across runs.** The symbol table is built per
  agent, in memory (§4.3). A persistent, ranked index is PRD 05.
- **Parsing every language exactly.** Go is exact. The others are as good
  as ctags, or as good as a line heuristic when ctags is absent.

## 3. Part 1: the `outline` package and an exported walk

This part ships first and alone. It adds no tool. agent-fox's repository
map depends on it.

**`outline`**, a new package below the root (ADR 01: it imports only the
standard library and `core`):

```go
type Decl struct {
    Kind      string // "func", "method", "type", "class", "interface", "const", "var", "module", …
    Name      string
    Container string // receiver or enclosing type/class, "" at top level
    Signature string // one line, at most 200 bytes, as written in the source
    Exported  bool   // the language's notion; true where it has none
    StartLine int    // 1-based, inclusive
    EndLine   int    // 1-based, inclusive; 0 when the backend cannot tell
}

type File struct {
    Path    string // workspace-relative, slash-separated
    Lang    string
    Backend Backend // "go/ast", "ctags", "heuristic", "none"
    Decls   []Decl  // sorted by StartLine
}

func Outline(ctx context.Context, abs string, src []byte, opts Options) (File, error)
```

Backends, tried in order for each file:

| Backend | Languages | How |
|---|---|---|
| `go/ast` | Go | `go/parser` with `SkipObjectResolution`. Top-level decls, methods with receivers, struct and interface types; exact ranges from `token.FileSet`. |
| `ctags` | everything universal-ctags knows | `ctags --output-format=json --fields=+neKS -f - <file>`, located with `exec.LookPath` the way `search_files` locates `rg`. Run under `ReducedEnv`, the context's deadline, and the same process-group kill as `execute`. Batched, many files per process, for the symbol table. |
| `heuristic` | Python, JS/TS, Rust, Java, Kotlin, C#, Ruby, C/C++ | Anchored line regexes for top-level declarations (`^def `, `^class `, `^export (async )?function `, `^(pub )?fn `, …). `EndLine` is 0. |
| `none` | anything else | No declarations, but the file is still in the walk. |

`Options` carries a per-file byte cap (1 MiB by default). A larger file is
returned with `Backend: "none"` and no declarations, as `read_file` does
for oversized input.

**The exported walk.** `tools` gains

```go
func Walk(ctx context.Context, ws *Workspace, root string, opts IgnoreOptions,
    fn func(rel string, d fs.DirEntry) error) error
```

Today the walk exists three times: `find_files` (`tools/tools.go`) and both
`search_files` backends (`tools/search.go`) each run their own
`filepath.WalkDir` over `newIgnoreEngine`. Part 1 replaces those three loops
with one exported `Walk`. It keeps the same ignore layers, including global
excludes and the no-repository case, and the same hidden-entry rule, and it
is the only walk afterwards. The existing `find_files` and `search_files`
suites, unchanged, are the proof that behaviour did not move.

## 4. Part 2: the tools

### 4.1 `file_outline`

```
file_outline(path: string, include_private?: bool)
```

It returns the file's declarations as compact text, grouped under their
container:

```
internal/agentrun/phase.go  (go/ast, 14 declarations)
  L93-107    type Observer interface
  L148-187   type Phase struct
  L190-207   type Result struct
  L215-217   type Runner struct
  L222-236   func NewRunner(cfg Config) (*Runner, error)
  L240-312   func (r *Runner) Run(ctx context.Context, p Phase) (Result, error)
```

- `include_private` defaults to false. Unexported declarations are counted
  in the header but not listed.
- `Data` carries the `outline.File` for the embedder. `Text` is what the
  model sees (the `RenderSearchText` precedent).
- Paths go through `Workspace.Resolve`. A directory, a non-regular file or
  an ignored path is refused with the same codes `read_file` uses.
- `PromptGuidelines`: *"Before reading a large file, outline it and read
  only the range you need."*

### 4.2 `find_symbol`

```
find_symbol(name: string, kind?: string, path?: string, exact?: bool, max_results?: int)
```

- `name` matches the declaration name. By default the match is prefix and
  smart-case. `exact: true` requires an exact, case-sensitive match. A
  qualified `Runner.Run` matches `Container`+`Name`.
- `kind` filters by `Decl.Kind`. `path` restricts the search to a
  subdirectory.
- Results are ranked: exact before prefix, then exported before
  unexported, then non-test files before test files, then shorter paths
  first. Ties are broken by path and line, so the order is deterministic.
- `max_results` defaults to 20 and is capped at 50. A truncated result
  carries the REQ-TOOL-09 marker naming `max_results`.
- `PromptGuidelines`: *"Use find_symbol to locate a declaration;
  search_files for usages and text."*

### 4.3 The symbol table and its freshness

- The table is built lazily, on the first `find_symbol` call. It covers
  every file the walk yields, using the backends in §3. It is held on the
  `fileTools` value, so it lives exactly as long as the agent's tool set.
- **It is never stale.** Every `write_file` and `edit_file` already takes
  the per-path lock (REQ-LOOP-12). On release, the path's entry is marked
  dirty. `execute`, `run_command` and `powershell` can change any file, so
  they mark the whole table for revalidation. Before answering, the tool
  re-outlines the dirty paths. On a revalidation it compares
  `(size, mtime)` for every walked file and re-outlines only the files that
  changed. A file deleted since is dropped.
- **Bounds.** At most 50 000 files and 2 s of wall time per build. When
  either bound is hit, the result carries `partial: true` and a note telling
  the model to narrow with `path` or fall back to `search_files`. The bounds
  are `Options` fields.

### 4.4 Where it shows up

- Both tools are in `All()`. `FileNavigationTools()` grows from three names
  to five, and the REQ-TOOL-04e execute fallback treats them like the
  others.
- Both are read-only. They report no write capability to the guard, so an
  embedder's read-only invariant (agent-fox's `AssertReadOnly`) needs no
  change, only the two names added to its allowlist.
- Every result carries `backend` (in `Data`, and in the text header) and
  `partial` when it applies.

## 5. Security and robustness

- Input bounds (REQ-SEC-11 style): `name` at most 256 bytes. `kind` must be
  one of a closed set. A `path` outside the workspace is refused before any
  walk.
- ctags is run on workspace files only, with an explicit file list (never
  `-R`), a reduced environment, no shell, and `--options=NONE`, so a
  repository's `.ctags.d/` cannot add a parser that executes code. Output is
  bounded by the same accumulator `execute` uses. Malformed JSON lines are
  skipped and counted, never trusted.
- Signatures are truncated to 200 bytes and stripped of control characters
  before rendering.

## 6. Tests

- `outline`: golden files per language and backend, under
  `outline/testdata/`. Go ranges are checked against `go/ast` directly.
  ctags tests skip when `ctags` is not on PATH, as the ripgrep tests do
  without `rg`.
- The exported walk: the existing `find_files` and `search_files` suites,
  unchanged, plus one test that `Walk` and `find_files **` yield the same
  set.
- Freshness: edit a file through `edit_file` and verify `find_symbol` sees
  the new declaration. Delete it through `execute` and verify it disappears.
- Bounds: a synthetic 60 000-file tree returns `partial: true` within the
  deadline.
- `internal/policy`: unchanged and green. That is the REQ-GO-11 acceptance.
- Cross-target: `outline` builds on all four `crossTargets`. ctags is
  optional at run time, never at build time.

## 7. Documentation

- `docs/api.md`: the `outline` package and `tools.Walk`.
- `docs/architecture.md`: `outline` in the package graph, below the root.
- README: the two tools in the tool list, and references and callers under
  *What is not built*.
- `docs/GAPS.md`: the same gap, with a pointer to this PRD's §2.
