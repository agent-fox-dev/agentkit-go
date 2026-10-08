# Add a find_references tool

Status: **proposed**. Where this sits relative to agent-fox, and the order the
two repositories move in, is recorded in agent-fox
[ADR 07](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/adr/07-split-code-navigation-between-agentkit-and-agent-fox.md).
agent-fox's own statement of the need is
[PRD 09](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prds/09-add-a-find-references-tool.md);
its §1 to §5 are this PRD, and its §6 (which phases get the tool, the prompt
wording, `--preflight`) is agent-fox's alone. It builds on
[PRD 04](04-add-symbol-navigation-tools.md) (`outline`, `file_outline`,
`find_symbol`) and is independent of
[PRD 05](05-add-an-indexed-code-search-module.md) (`code_search`).

## 1. The problem

After PRD 04, a model can ask what a file declares (`file_outline`) and where
a name is declared (`find_symbol`). It cannot ask the third question a reader
of unfamiliar code asks: **who calls this?** PRD 04 named references and
callers a non-goal and left them to `search_files` with `\bX\b`. That answer
is weak in three ways a model pays for in turns:

- It returns declarations, comments, strings and same-named identifiers from
  other packages alongside the calls, and the model reads files to tell them
  apart.
- It says which *line* mentions the name, not which *function* is calling. A
  caller is a declaration, and attributing a hit to its enclosing declaration
  is exactly what the outline already knows and a regex does not.
- It cannot see a call through a receiver, an interface method or a renamed
  import, which is where the behaviour that matters in a harness tends to
  live.

The same data is useful without a model. A repository map that ranks files by
how much depends on them, or a handbook phase that builds a call graph, needs
call edges as a program fact. Today an embedder would have to write its own
type-checker pass to get them.

## 2. Goals and non-goals

**Goals.**

1. A model can list the call and reference sites of a declaration across the
   workspace in one call, each site attributed to the declaration that
   contains it, ranked so that resolved calls come first and text matches
   last.
2. For Go, the answer is exact within the workspace: a method call through a
   receiver, an interface method and a renamed import resolve to the right
   declaration, and a same-named identifier in another package does not.
3. For every other language, the answer is at least as good as `search_files`
   and better in one way: every hit names its enclosing declaration.
4. An embedder can get the same data without a model, through an exported
   function on `tools.Workspace`.
5. The root module stays standard-library-only and cgo-free (REQ-GO-11,
   NFR-COMPAT-06). Go resolution uses `go/parser` and `go/types`; nothing from
   `golang.org/x/tools`. Both `internal/policy` tests stay green with no new
   entry in `allowedModules`.
6. Results never disagree with the other file tools about which files exist.
   The tool walks through the same ignore engine, hidden-entry rule and
   `Workspace` confinement as `search_files` and `find_symbol`.

**Non-goals.**

- **A language server client.** Resolution is a Go type-check over workspace
  sources with external imports stubbed. It is not `gopls`.
- **Exact resolution for languages other than Go.** ctags and the heuristic
  outline give enclosing declarations; they do not give types.
- **Cross-module resolution.** A call from the workspace into a dependency, or
  from a dependency into the workspace, is out of scope. The question is
  always "who, in this repository, uses this".
- **A persistent index.** The reference table lives as long as the agent's
  tool set, like the symbol table (PRD 04 §4.3).
- **Changing `find_symbol`, `search_files` or `file_outline`.** Their schemas,
  backends and results are unchanged.
- **Call edges in any repository map.** That is agent-fox's decision, made on
  the numbers this tool produces.
- **Which agent-fox phases get the tool.** That is policy and stays in
  agent-fox (ADR 07).

## 3. The `find_references` tool

```
find_references(name: string, path?: string, kind?: string, include_tests?: bool, max_results?: int)
```

- `name` is a declaration name, optionally qualified as `Container.Name`
  (`Runner.Run`). It is resolved through the symbol table first. When it
  matches no declaration, the tool answers with `text` sites only and says so
  in the header.
- `path` restricts the search to a subdirectory, as it does on `find_symbol`.
  `kind` filters the *referenced* declaration when the name is ambiguous
  (`func` vs `type`), with the same closed set `find_symbol` uses.
  `include_tests` defaults to true; false drops hits in files the language's
  test convention names.
- `max_results` defaults to 30 and is capped at 100. A truncated result
  carries the REQ-TOOL-09 marker naming `max_results`.
- Results are grouped by file and rendered as compact text, the
  `find_symbol` precedent:

  ```
  find_references Runner.Run  (go/types, 7 references in 4 files; 0 text matches)
  codefix/phases.go
    L212  resolved  func runAnalysis(...)        res, err := r.Run(ctx, phase)
    L301  resolved  func runImplement(...)       res, err := r.Run(ctx, phase)
  internal/agentrun/phase_test.go
    L88   resolved  func TestRunRejectsUnknown   _, err := r.Run(ctx, p)
  ```

  Each site carries the line, a confidence, the enclosing declaration from
  the outline (or `<file>` when the hit is at top level), and the trimmed
  source line, capped at 200 bytes and stripped of control characters.
- `ToolResult.Data` carries the structured sites: path, line, column,
  confidence, enclosing `outline.Decl`, and the referenced declaration. The
  header and `Data` both carry the backend and, when it applies, `partial`.
- `PromptGuidelines`: *"Use find_symbol for where a name is declared,
  find_references for who uses it, and search_files for text."* The guideline
  mentions no tool outside the navigation set and not `execute`, so an
  embedder's read-only phases keep it.

### 3.1 Confidence, and the three backends behind it

Every site carries one of three confidence values, and the header counts each.
Confidence is the honest version of PRD 04's "exact parity between backends"
non-goal: the tool does not pretend one answer, it labels them.

| Confidence | Backend | Languages | What it means |
|---|---|---|---|
| `resolved` | `go/types` | Go | The identifier at this position type-checks to the declaration named. Receiver calls, interface methods, renamed imports and embedded fields resolve. Same-named identifiers that resolve elsewhere are excluded. |
| `lexical` | symbol table + tokenizer | every language the outline covers | An identifier-boundary match of the name, in a non-comment, non-string position where the backend can tell, attributed to the enclosing declaration from the outline. |
| `text` | `search_files`'s matcher | everything else | A `\bname\b` match in a file the outline could not parse, or in a comment or string. |

**Go resolution.** The workspace's Go packages are parsed with `go/parser` and
checked with `go/types` using a workspace-local importer: a package under the
workspace is checked from source; any other import path (standard library
included) is answered with a stub package that defines nothing,
`Config.Error` collects rather than aborts, and `FakeImportC` is set. The
check is therefore incomplete by design and never fails the call. An
identifier that cannot be resolved because its type came from outside the
workspace is reported as `lexical`, not dropped. Build-tag variants are checked
under the host's default tags only. The checked packages are cached on the
tool set and invalidated the way the symbol table is (§3.3).

**Candidate search.** Both lower backends start from the files that mention
the name. When `tools.Options.Index` is set (PRD 05), the candidate list comes
from the index; otherwise from the same walk `search_files` uses. Either way
the result is the same set, which the `search_files` suites already establish
for ripgrep versus native.

### 3.2 Ranking and bounds

- Order: `resolved` before `lexical` before `text`; within a confidence,
  non-test files before test files, then path, then line. Deterministic.
- Input bounds (REQ-SEC-11 style): `name` is at most 256 bytes. `kind` is a
  closed set. A `path` outside the workspace is refused before any walk, with
  `read_file`'s codes.
- The type-check is bounded by `SymbolOptions.MaxFiles` and
  `SymbolOptions.MaxDuration` (50 000 files, 2 s by default). No new option is
  added. When a bound is hit, the result carries `partial: true` and a note to
  narrow with `path`, and the sites found so far are still returned,
  downgraded to `lexical` where the check did not finish.

### 3.3 Freshness

The reference table shares the symbol table's freshness rules (PRD 04 §4.3):

- `write_file` and `edit_file` mark the written path dirty on release of the
  per-path lock (REQ-LOOP-12).
- `execute`, `run_command` and `powershell` mark the whole table.
- A dirty Go package is re-checked before the next answer; a dirty
  non-Go file is re-outlined.
- A file deleted since the last answer is dropped.

An embedder that builds a fresh tool set per phase, as agent-fox does, is
covered by construction.

### 3.4 Where it shows up

- The tool is in `All()`. `FileNavigationTools()` grows from five names to
  six, and the REQ-TOOL-04e execute fallback treats it like the others.
- It is read-only. It reports no write capability to the guard, so an
  embedder's read-only invariant (agent-fox's `AssertReadOnly`) needs no
  change, only the name added to its allowlist.

## 4. The embedder seam

```go
type ReferenceOptions struct {
    Path         string // restrict to a workspace subdirectory; "" for all
    IncludeTests bool
    MaxResults   int // zero or negative means 30; capped at 100
}

type ReferenceSite struct {
    Path       string // workspace-relative, slash-separated
    Line       int    // 1-based
    Column     int    // 1-based, in bytes
    Confidence string // "resolved", "lexical" or "text"
    Enclosing  outline.Decl // zero Decl, with Kind "file", at top level
    Source     string // trimmed, at most 200 bytes, no control characters
}

type ReferenceResult struct {
    Target          outline.Decl
    Sites           []ReferenceSite
    Backend         string
    Partial         bool
    PackagesChecked int // Go packages the type-check covered
    Errors          int // type errors collected, expected because imports are stubbed
    Truncated       bool
}

func (ws *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error)
```

`References` returns the same sites `find_references` renders, so an embedder
can enumerate callers for a repository map or a handbook phase without a model
turn. It also reports `PackagesChecked`, `Errors` and `Partial`, so an embedder
can tell whether the Go type-check ran under its bounds without parsing
rendered text; agent-fox's `--preflight` probe uses them. It honours the same
bounds as the tool and returns the result found so far with `Partial` set when
`ctx` ends first. It is the only new exported API: the type-checker, the
importer and the tokenizer stay unexported.

The field list above is the design target. The spec that implements this PRD
fixes the final names and verifies them against the code.

## 5. Security and robustness

- The type-check reads workspace sources only. The importer never reads
  `GOROOT`, `GOPATH`, the module cache or the network, and never runs
  `go list`, `go build` or cgo. An import outside the workspace gets an empty
  stub package.
- Nothing from the workspace is executed. ctags, when used for the enclosing
  declarations, runs under the PRD 04 rules: explicit file list, reduced
  environment, no shell, `--options=NONE`, bounded output, malformed lines
  skipped and counted.
- Source lines and signatures are truncated to 200 bytes and stripped of
  control characters before rendering.
- A crafted workspace cannot make the check unbounded: file count, wall time
  and `max_results` all bound the work, and a panic in a checker pass is
  recovered and reported as `partial`.
- All paths go through `Workspace` confinement. Symlinks that leave the
  workspace are not followed, as in `search_files`.

## 6. Tests

- **Go resolution**, fixtures under `tools/testdata/`: a method call through a
  receiver, a call through an interface value, a call through a renamed
  import, an embedded-field promotion, and a same-named method on an
  unrelated type that must not appear. Each is pinned by its own fixture.
- **Acceptance on this repository:** `find_references Runner.Run` returns
  every call of the right `Run` as `resolved`, attributed to its enclosing
  function, and no hit for an unrelated `Run` method.
- **Other languages:** a Python fixture with ctags present gives every hit its
  enclosing `def` or `class`; with ctags absent, the heuristic backend gives
  the same enclosing declarations for top-level functions. ctags tests skip
  when `ctags` is not on PATH.
- **No declaration:** a name that matches nothing returns `text` sites only,
  and the header says so.
- **Bounds:** a 60 000-file synthetic tree returns `partial: true` within the
  deadline with the sites found so far. `max_results` truncation carries the
  REQ-TOOL-09 marker.
- **Freshness:** editing a caller through `edit_file` changes the next answer;
  deleting it through `execute` removes the site.
- **Parity of candidates:** with and without `Options.Index`, the same sites.
- **Seam:** `Workspace.References` returns the sites the tool renders, and
  reports `PackagesChecked`, `Errors` and `Partial` consistently with the
  header.
- **Determinism:** two runs over the same tree yield byte-identical output.
- **`internal/policy`:** unchanged and green. That is the REQ-GO-11
  acceptance. `tools` builds on all four `crossTargets`.
- The existing `file_outline`, `find_symbol` and `search_files` suites pass
  unchanged.

## 7. Order

1. This PRD, as one AgentKit spec covering §3 to §4, after
   `02_symbol_navigation_tools` (merged). It does not wait for
   `03_indexed_code_search`.
2. agent-fox's wiring (its PRD 09 §6, specs `17` and `18` there) starts after
   this spec is merged and the `replace` target carries it. agent-fox's task
   gate greps `tools/` for `find_references` and stops if it is absent.

Each step leaves `make check` green in its repository on its own.

## 8. Documentation

- `docs/api.md`: `Workspace.References` and its option and result types.
- `docs/architecture.md`: the reference table beside the symbol table.
- README: `find_references` in the tool list; *What is not built* drops
  "references and callers".
- `docs/GAPS.md`: the "Symbol references and callers" row moves from
  *Deferred* to *Fixed*, citing the spec.
