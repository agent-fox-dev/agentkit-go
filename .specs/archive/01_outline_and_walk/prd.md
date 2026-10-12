---
spec_id: "01"
spec_name: "outline_and_walk"
title: "Outline package and a single exported workspace walk"
status: "active"
created_at: "2026-10-01T15:19:08.067618Z"
updated_at: "2026-10-01T15:19:08.067618Z"
intent_hash: "8d30a452d2675cd988c7eaef214d0c37d2991379f131959490a66cee9e2b9e08"
schema_version: 2
source: "docs/prd/04-add-symbol-navigation-tools.md"
---
## Intent

Give AgentKit a reusable, standard-library-only way to list the declarations of a source file with exact or best-effort line ranges (the `outline` package), and collapse the three private directory walks in `tools` into one walk that is also exported, so that any consumer of workspace files sees the same set of files the file tools see. This spec adds no model-facing tool; it is the data and traversal foundation that later navigation tools and embedders build on.

## Goals

1. `outline.Outline` returns a `File` with sorted `Decl`s for a Go file, with ranges taken from `go/ast`, and does so without any subprocess.
2. For other languages it returns declarations from universal-ctags when a ctags runner is supplied and usable, from anchored line heuristics for ten named languages when it is not, and an empty declaration list with `Backend: "none"` otherwise. Every `File` names the backend that produced it.
3. `outline.OutlineMany` outlines many files with one ctags process per batch, not one per file.
4. `tools` has exactly one directory-walk implementation. `find_files`, the native `search_files` backend and `CountCandidates` use it, and `tools.Walk` exposes it behind workspace confinement. The existing `find_files` and `search_files` test suites pass unchanged.
5. `tools.CtagsRunner` supplies the ctags process lifecycle (reduced environment, context deadline, process-group kill, bounded output), so `outline` needs no first-party import beyond the standard library.
6. The root module stays standard-library-only and cgo-free. `internal/policy` tests pass with no new entry in `allowedModules`, and `outline` builds and vets on all four `crossTargets`.

## Non-goals

- The `file_outline` and `find_symbol` tools, the in-memory symbol table, its freshness tracking and its file and time bounds. These are the second scope of this split, `symbol_navigation_tools`. `FileNavigationTools()`, `All()` and the pinned default tool set in `tools/reqtool06_test.go` do not change in this spec.
- References, callers, type resolution and any language-server client.
- Equal output from the three backends. The backend is named on every `File` so that a disagreement can be diagnosed.
- A persistent or ranked symbol index.
- Parsing non-Go languages exactly. Heuristic declarations cover column-0 declarations only, and ctags coverage is whatever the installed ctags provides.
- Changing which entries `find_files` or `search_files` return. The refactor must preserve both tools' behaviour, including the difference in how they treat dot-prefixed entries (see Design Decision 4).
- Any change to the ripgrep path of `search_files`, which delegates traversal to `rg`.

## Background

AgentKit's file tools work on paths and text. A coding agent asking "what is in this file" or "where is X declared" has to read files or write a regex. agent-fox's repository map needs the same declaration data without writing its own traversal or `.gitignore` engine. The `docs/prd/04-add-symbol-navigation-tools.md` proposal splits this into a package-and-walk part that ships alone and a tools part. This spec is the first part.

What exists today, from reading `tools/`:

- `tools/tools.go`: `find_files` runs its own `filepath.WalkDir` over `newIgnoreEngine`, applies `ig.match`, calls `ig.enter` on each kept directory, and does **not** skip dot-prefixed entries (only `.git` is ignored, inside `ignoreEngine.match`). Its `limit` and `file_type` handling stay in the tool.
- `tools/search.go`: `searchNative` and `countCandidates` each run their own `filepath.WalkDir`. Both skip entries where `isHidden(d.Name())` or `ig.match` is true, and skip non-regular files. The ripgrep backend (`searchRipgrep`) does not walk; it runs `rg` and calls `countCandidates` for `files_searched`. The walk is therefore duplicated three times (find_files, searchNative, countCandidates), not once per backend.
- `tools/ignore.go`: the layered ignore engine (`newIgnoreEngine`, `enter`, `match`), `IgnoreOptions`, `NoGlobalExcludes()`.
- `tools/path.go`: `Workspace`, `Resolve`, and the unexported `within`. `Search` and `SearchIn` take a root string with no `Workspace`, so a walk shared with them cannot require one.
- `tools/exec.go`: `RunArgv` runs a program with a process group and a bounded accumulator. It merges stderr into stdout and truncates from the tail, which would corrupt a JSON stream. `ReducedEnv`, `setProcessGroup` and `killGroup` (with `proc_unix.go` and `proc_windows.go`), and `Accumulator` (`NewAccumulator(cap, TruncateHead)`, `Truncated()`, `Total()`) are reusable pieces.
- `docs/adr/01-keep-the-root-package-to-the-agent.md` requires every package other than `subagent` to sit below the root and import only `core` and siblings. `tools` imports `core`, `imagex` and `schema`; nothing below `tools` may import it.
- `internal/policy/deps_test.go` pins `allowedModules` to the module itself, and `crosstarget_test.go` builds and vets linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with `CGO_ENABLED=0`.

## Requirements

### 1. The `outline` package and its data model

`outline` is a new package at the module root's level, in `outline/`. It imports only the standard library (it needs nothing from `core`). It defines:

- `Decl` with `Kind`, `Name`, `Container`, `Signature`, `Exported`, `StartLine` (1-based, inclusive) and `EndLine` (1-based, inclusive, 0 when the backend cannot tell).
- `File` with `Path` (slash-separated, relative to `Options.Root`, or the slash-converted absolute path when `Root` is empty), `Lang`, `Backend` and `Decls`. `Decls` is sorted by `StartLine`, ties broken by `Name`, and is never nil.
- `Backend`, a string type with constants `"go/ast"`, `"ctags"`, `"heuristic"` and `"none"`.
- `Options` with `Root`, `MaxFileBytes` (zero means 1 MiB), and `Runner`, the ctags seam (requirement 3).
- `Source` with `Abs` and an optional `Src []byte`, and `Stats` with counts (malformed ctags lines skipped, batches that fell back to another backend).

`Kind` takes a closed set of values: `func`, `method`, `type`, `class`, `interface`, `enum`, `trait`, `const`, `var`, `module`, `macro`. Backends map their own vocabulary into it and drop declarations that do not map (locals, parameters, fields, struct members, enumerators, labels, imports). Only top-level declarations and methods of a declared type are kept.

`Signature` is one line, as written in the source: newlines and runs of whitespace collapsed to one space, control characters removed, cut at 200 bytes on a rune boundary. This is done in `outline`, so every consumer gets sanitised signatures.

`Outline(ctx, abs, src, opts)` returns one `File`. `OutlineMany(ctx, srcs, opts)` returns a `[]File` in input order and a `Stats`. When `Source.Src` is nil, `outline` reads the file itself. Language is chosen by file extension from a fixed table in the package: the Go, Python, JS/TS, Rust, Java, Kotlin, C#, Ruby and C/C++ extensions, plus a short list of further extensions universal-ctags handles well (for example PHP, Swift, Scala, Lua, shell, Perl). An extension outside the table gives `Lang: ""` and `Backend: "none"`, and the file is not read.

A file larger than `MaxFileBytes`, or containing a NUL byte in its first 8 KiB, is returned with `Backend: "none"` and no declarations. A cancelled context returns `ctx.Err()`. Unreadable files return an error from `Outline`; `OutlineMany` returns that file as `none` and continues.

### 2. The Go backend

Go files are parsed with `go/parser.ParseFile` using `parser.SkipObjectResolution`. Declarations kept: functions (`func`) and methods (`method`, with `Container` set to the receiver's base type name, without `*` and type parameters), type specs (`interface` for interface types, `type` for every other type), and const and var specs (`const`, `var`, one `Decl` per name, skipping `_`). `StartLine` and `EndLine` come from the node positions in the `token.FileSet`; a decl's range starts at its keyword (or at the spec inside a grouped declaration) and does not include the doc comment. `Exported` is `ast.IsExported(name)`. Signatures are `func ...` up to the body, `type Name struct`, `type Name interface`, or `type Name <underlying type expression>` on one line for other types, and `const Name` / `var Name [Type]`.

If the file has syntax errors, the partial AST that the parser returns is still used and the backend stays `go/ast`. If no AST is returned at all, the file falls through to the next backend as for any other language.

### 3. The ctags backend and its seam

`outline` does not import `tools`, so it cannot call the process helpers directly. `Options.Runner` is a function `func(ctx context.Context, args []string) (stdout []byte, err error)` that runs ctags with the given arguments (without the program name) and returns its standard output. A nil `Runner` disables the ctags backend; `outline` never spawns a process itself.

For files in the extension table that are not Go, and when `Runner` is non-nil, `outline` calls it with this argument list: `--options=NONE` first (so no preload or `.ctags.d` file is read), then `--output-format=json`, `--fields=+neKS`, `--sort=no`, `-f`, `-`, then an explicit list of absolute file paths. It never passes a directory or `-R`. Batches hold at most 100 files and 32 KiB of path text, and absolute paths guarantee that no file name begins with `-`.

Output is read as JSON lines. Lines whose `_type` is not `tag` are ignored. A line that is not valid JSON or lacks a name, path or line is skipped and counted in `Stats`, never trusted. Tags are attributed to files by the `path` they echo; a path that was not in the batch is dropped. Kinds are normalised into the closed set; a function-kind tag whose scope kind is type-like (class, struct, interface, enum, trait, impl) becomes `method` with `Container` set to the scope. `EndLine` comes from ctags' `end` field when present, else 0. `Signature` is the trimmed source line at `StartLine`; ctags' `signature` field is used only when that line is unavailable. `Exported` is true unless the name begins with an underscore.

If the `Runner` returns an error, or the batch's output is not usable, every file in that batch is handled by the heuristic backend if its language has one, and otherwise returned as `none`; `Stats` records the fallback. The error is not returned to the caller of `OutlineMany`, because a missing or broken ctags is an ordinary condition, not a failure of the outline. A cancelled context is the exception and returns `ctx.Err()`.

### 4. The heuristic and `none` backends

For Python, JavaScript, TypeScript, Rust, Java, Kotlin, C#, Ruby, C and C++, the heuristic backend applies anchored line regular expressions to declarations that start at column 0, optionally after a modifier such as `export`, `export default`, `async`, `pub`, `pub(crate)`, `public` or `static`. Examples: `^def `, `^class `, `^export (async )?function `, `^(pub )?fn `. `Container` is empty and `EndLine` is 0. `Exported` is true unless the language marks otherwise: a leading underscore in Python and Ruby, a missing `pub` in Rust, a missing `export` in JavaScript and TypeScript, a missing `public` in Java, Kotlin and C#; it is true for C and C++. Indented declarations, such as methods inside a class, are not reported; a consumer wanting them needs ctags. A language with no heuristic and no usable ctags yields `none`, and the file stays in any walk that listed it.

### 5. The ctags runner in `tools`

`tools` exports `CtagsRunner(env []string) func(ctx context.Context, args []string) ([]byte, error)`. The returned function is assignable to `outline.Options.Runner`. Its behaviour:

- It locates `ctags` with `exec.LookPath`, once per runner, and confirms it is universal-ctags by running `ctags --version` and checking for `Universal Ctags` in the output. Exuberant and BSD ctags (the macOS system one) fail this check. When ctags is absent or not universal, every call returns an error wrapping an exported sentinel `ErrCtagsUnavailable`, without spawning anything further.
- The environment is `env`, or `ReducedEnv(nil)` when `env` is nil.
- stdin is empty, stderr is discarded, and stdout is captured separately from stderr into an `Accumulator` in `TruncateHead` mode with a 16 MiB cap. If the accumulator reports truncation the call returns an error rather than a cut-off stream.
- The process runs in its own process group (`setProcessGroup`) and the group is killed with `killGroup` when the context ends, as `execute` does.
- A non-zero exit status is an error.

No `Runner` is installed by default anywhere in this spec. An embedder opts in with `outline.Options{Runner: tools.CtagsRunner(nil)}`.

### 6. One walk

`tools` gets a single unexported walk implementation, taking a root, an `IgnoreOptions`, a hidden-entry switch and a callback. It does what the three loops did: build `newIgnoreEngine(root, opts)`; for each entry other than the root, compute the slash-separated path relative to the root; skip (`SkipDir` for a directory) any entry the ignore engine matches, and any dot-prefixed entry when hidden entries are excluded; call the callback; and, for a directory the callback did not skip, call `ig.enter` so that its ignore files apply to its children. The callback is called for directories as well as files. Unreadable entries are skipped without error, and a root that does not exist yields no calls and a nil error, as `find_files` and `search_files` behave today. The context is checked per entry and a cancelled context returns `ctx.Err()`. The callback returns `filepath.SkipDir`, `filepath.SkipAll` or an error with the usual `fs.WalkDirFunc` meaning; `SkipAll` ends the walk with a nil error.

`tools.Walk(ctx, ws, root, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error` is the exported entry point. `WalkOptions` holds an `Ignore IgnoreOptions` and `IncludeHidden bool`. `Walk` returns an error wrapping `ErrPathNotAllowed` when `ws` is nil, when `root` is not absolute, or when `root` lies outside `ws.Root` (compared with `within`), before any entry is visited. It does not follow symlinks, because it uses `filepath.WalkDir`. The callers that already resolved a root through `Workspace.Resolve` (the `find_files` tool) call `Walk`; `Search`, `SearchIn` and `CountCandidates`, which take a root string with no workspace, call the unexported implementation directly.

### 7. Callers keep their behaviour

`find_files` calls the walk with hidden entries included and keeps its own `file_type`, glob, limit, truncation marker and sorting. The native search backend and `countCandidates` call it with hidden entries excluded, and keep their regular-file check, `file_glob` filter and match logic. No tool's description, schema, result shape or marker text changes. The existing `find_files` and `search_files` tests, including the ripgrep/native parity test, pass without edits.

### 8. Dependency, build and platform constraints

`outline` contains no build-constrained files, no cgo and no import outside the standard library. `go.mod` gains no `require`. `internal/policy` tests are unmodified and pass. `go build` and `go vet` succeed for all four `crossTargets`. ctags is a run-time option only; no code path requires it to build or to pass the default test run.

### 9. Tests

- `outline` golden files under `outline/testdata/`, one per language and backend: Go, plus heuristic fixtures for each of the ten heuristic languages, plus a `none` case. Go ranges are additionally checked by parsing the same source with `go/ast` in the test and comparing.
- ctags parsing and batching are tested with a fake `Runner` returning canned JSON, including malformed lines, unknown paths, non-`tag` lines, a runner error (heuristic fallback) and a cancelled context. The argument list is asserted, including `--options=NONE` first, `-f -`, no `-R`, and absolute paths only.
- Tests that run real ctags skip when `ctags` is not on `PATH` or is not universal-ctags, in the manner of the ripgrep tests.
- `CtagsRunner` is tested for the `ErrCtagsUnavailable` path (empty `PATH`) and, where ctags is present, for process-group kill on context cancellation and for the truncation error with a lowered cap.
- Walk: one test that `Walk` with hidden entries included and `find_files` with pattern `**` and `file_type` `file` yield the same set over a tree with nested `.gitignore` files, a nested repository, a hidden file and a symlink; one test that the hidden switch off matches the file set `search_files` selects; one test that a root outside the workspace is refused with `ErrPathNotAllowed`; and one for `SkipDir`, `SkipAll` and context cancellation.
- Boundary cases: file over `MaxFileBytes`, a file with a NUL byte, a signature longer than 200 bytes, a signature containing control characters and a multi-byte rune at the cut.

### 10. Documentation

In the same change: `docs/architecture.md` adds `outline` to the package graph (imports: nothing first-party) and the packages table, and notes `tools.Walk` and `tools.CtagsRunner` under `tools`; `README.md` adds a short entry in the section that describes the `tools` package and keeps the "What is not built" list accurate (references and callers remain unbuilt); `docs/GAPS.md` records the walk consolidation. `docs/api.md` is the network API document and does not change. `make check` passes after the documentation edits.

## Design Decisions

1. **Split into two specs.** The input already says Part 1 ships first and alone and that agent-fox depends on it, and Part 2 adds two tools, a stateful table, freshness hooks and bounds, which is more than one spec's size. This spec is Part 1; Part 2 is `symbol_navigation_tools`.
2. **`outline` gets its ctags process through an injected `Runner`, not by importing `tools`.** The input says `outline` imports only the standard library and `core`, but the process-group kill, `ReducedEnv` and the accumulator live in `tools`, and the later tools in `tools` will import `outline`; a direct import would be a cycle and violate ADR 01.
3. **`tools.CtagsRunner` is a function returning an unnamed func type, in `tools`.** It reuses `setProcessGroup`, `killGroup`, `ReducedEnv` and `Accumulator` rather than copying them (steering: reuse before writing), and the unnamed type means `tools` does not need to import `outline` in this spec.
4. **`find_files` keeps showing dot-prefixed entries; the shared walk takes a switch.** The input says the shared walk keeps "the same hidden-entry rule", but the two tools differ today: `find_files` returns `.github/…` and `.gitignore`, while `search_files` skips them. One rule would change a tool's behaviour and break the "suites unchanged" proof, so `WalkOptions.IncludeHidden` selects which rule applies. Symbol consumers use the `search_files` rule.
5. **`Walk` has a `WalkOptions` parameter instead of a bare `IgnoreOptions`.** It is needed to carry the hidden switch from Decision 4; the field `Ignore` holds what the input's signature carried.
6. **An unexported walk underlies the exported `Walk`.** `Search`, `SearchIn` and `CountCandidates` take a root string and no `Workspace`, so they cannot call a function that requires one; confinement is added in the exported wrapper only.
7. **The callback sees directories as well as files.** `find_files` needs directories for `file_type: dir` and `any`, and a file-only callback would force it to keep its own loop.
8. **Closed `Kind` set, with Go structs as `type`.** A fixed vocabulary is needed so a later `kind` filter can validate input; `interface` is kept distinct because the input's own example output shows it, and struct-like kinds from other languages are normalised to `type` to match.
9. **ctags tags outside the closed set are dropped.** Struct fields, locals and enumerators would swamp an outline and do not answer "what is in this file"; methods keep a `Container`.
10. **Universal-ctags is verified by `--version`, not assumed from the binary name.** On macOS the `ctags` on `PATH` is BSD ctags, which rejects `--output-format=json`; treating it as available would make every outline fall back after a failed spawn.
11. **A ctags failure falls back to the heuristic for that batch and is not an error.** ctags is optional at run time, and a broken installation must not turn a navigation request into a failure. The fallback is counted in `Stats` and visible in each file's `Backend`.
12. **Truncated ctags output discards the whole batch.** The accumulator keeps a head window, and a half-cut JSON stream attributes tags to the wrong files or drops the tail's files silently; failing the batch and falling back is the honest result.
13. **Non-Go signatures are the source line at `StartLine`.** "As written in the source" is satisfied exactly by the source line, and it does not depend on a ctags field whose format varies by language. ctags' `signature` field is only a fallback.
14. **Heuristics report column-0 declarations only.** Indented matches need scope tracking that regexes cannot do reliably; the input specifies `EndLine` 0 and top-level declarations, and ctags exists for the rest.
15. **A syntax-broken Go file still gets `go/ast` declarations.** An agent usually asks for an outline while editing, when the file may not parse; the partial AST from `go/parser` is more useful than falling to a heuristic that does not exist for Go.
16. **`outline` reads the file when `Source.Src` is nil, and decides language by extension before reading.** This lets a symbol table stat-and-skip the many files with no known language without paying a read for each.
17. **`Options.Root` makes `File.Path` workspace-relative.** `Outline` receives an absolute path and has no workspace; the caller supplies the root, and an empty root yields the absolute path rather than a wrong relative one.
18. **Documentation goes to `architecture.md`, `README.md` and `GAPS.md`, not `api.md`.** The input names `docs/api.md`, but that file documents the HTTP network API (sections "Starting it", "HTTP endpoint", "Methods"), and the steering table reserves it for REST endpoints.
19. **No tool, default set or prompt change in this spec.** `TestTheDefaultToolSetIsPlatformStable` pins the tool list as the head of the cached prompt prefix, and the golden prompt tests depend on `FileNavigationTools()`; those move together with the tools in the second spec.

## Verified External API

All symbols are standard library; the Go toolchain source is outside the repository, so signatures below are from the standard library's documented API and are not read from a vendored copy. The repository declares `go 1.26.5` in `go.mod`.

| Symbol | Assumed signature | Status |
|---|---|---|
| `go/parser.ParseFile` | `func ParseFile(fset *token.FileSet, filename string, src any, mode Mode) (f *ast.File, err error)` | unverified (stdlib, not in repository) |
| `go/parser.SkipObjectResolution` | `const SkipObjectResolution Mode` | unverified |
| `go/token.NewFileSet`, `(*FileSet).Position` | `func NewFileSet() *FileSet`; `func (s *FileSet) Position(p Pos) Position` with `Position.Line int` | unverified |
| `go/ast.IsExported` | `func IsExported(name string) bool` | unverified |
| `os/exec.LookPath` | `func LookPath(file string) (string, error)`; already used in `tools/search.go` for `rg` | verified by existing call site |
| `tools.RunArgv`, `ReducedEnv`, `setProcessGroup`, `killGroup`, `NewAccumulator`, `(*Accumulator).Truncated` | as declared in `tools/exec.go`, `tools/proc_unix.go`, `tools/proc_windows.go` and `tools/accumulator.go` | verified (read) |
| universal-ctags JSON output | one JSON object per line with `_type` (`"tag"`), `name`, `path`, `line`, `kind`, and, with `--fields=+neKS`, `end`, `signature`, plus `scope` and `scopeKind` when the tag is nested | unverified (ctags cannot be run or read here; the parser must tolerate missing fields, which is why a line without name, path or line is skipped and counted) |
| ctags options `--options=NONE`, `--output-format=json`, `--fields=+neKS`, `--sort=no`, `-f -` | as specified in the input, except `--sort=no` which is added | unverified |
