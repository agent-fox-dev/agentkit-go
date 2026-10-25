# ADR 04: Describe the tools as they behave

- **Status:** Accepted
- **Date:** 2026-10-25 (records decisions taken between 2026-05 and 2026-09)

## Context

The built-in tools, the workspace they run in, code mode and the MCP client
were specified from PRDs that described the shapes and rules the authors
intended. Several tools already returned other shapes, the libraries
underneath (Starlark, tree-sitter, the Go type checker) imposed limits no spec
named, and moving the walk and containment code into `workspace` was meant to
change nothing a tool does. Where the specs and the code disagreed, the code's
existing behaviour was kept, declared, and recorded in an erratum. The errata
are gone; the rules the code follows are here.

## Decision

**Output schemas describe the `Data` the tools return.** Renaming keys would
change the result for every programmatic consumer, so each schema declares
what the tool marshals:

- `search_files` matches carry their context as `before` and `after`.
- `file_outline`'s `file` uses the Go field names of `outline.File` and
  `outline.Decl` (`Path`, `Lang`, `Backend`, `Decls`; `Kind`, `Name`,
  `Container`, `Signature`, `Exported`, `StartLine`, `EndLine`), all present.
- `find_symbol`'s `backends` is a count per backend; a symbol has a `backend`
  and no `score`, and all its fields are required.
- `find_references`' `target` is the `outline.Decl` searched for, `errors` is
  a count, and `sites` and `result` use the Go field names of
  `ReferenceSite` and `ReferenceResult`.
- `code_search` files carry `path`, `score`, `matches` (a count), `chunks` and
  `symbols` (null when no declaration starts on a matched line);
  `symbol_sources` is a count of files per backend and `dirty_files` a count.
- `find_files` returns `[]`, never `null`, for no match.
- `exec_failed` means the process never started, so it carries no `Data`.
- `edit_file` reports a rejected batch as `edit_<phase>` (`edit_empty`,
  `edit_empty_old_string`, `edit_not_found`, `edit_not_unique`,
  `edit_overlap`, `edit_noop`); `edit_failed` is a backstop for an error that
  is not an `*EditError`, which `ApplyEdits` never returns.

**Commands.** A timed-out call's `Duration` covers its `Timeout`: the timeout
context is created after the clock starts, never before.

**References.**

- A Go target is searched by type only: the resolver's identifiers are
  reported, and Go comments, strings and non-Go files mentioning the name are
  not scanned, so its header always shows `0 text matches`. A non-Go or
  undeclared target is scanned lexically, and its declaration line is reported
  as a `lexical` site.
- Comment and string spans come from tree-sitter, which needs cgo. Without
  cgo every non-Go hit is `text` and attributed to `<file>`.
- The time budget is checked per file walked, between Go package checks and
  between scanned files; walking the ASTs of packages already checked runs to
  completion.
- `tools.References` bounds its pass with the default limits (50,000 files,
  2 s) and type-checks from disk on every call; `find_references` uses the
  tool set's `SymbolOptions` and its reference cache. The results agree
  whenever neither reaches a bound.
- The Go loader skips files the workspace ignores, so a gitignored `.go` file
  is neither type-checked nor reported.

**The workspace.** The move into `workspace` kept the old walk exactly:

- `.git/info/exclude` applies only when the scan root itself contains `.git`.
  Walking a subdirectory of a repository applies the global layer and the
  subdirectory's own ignore files, not the root's.
- An unreadable directory is reported to the walk's callback once, and its
  contents never; the walk returns nil.
- `Rel` returns a path unchanged when `filepath.Rel` gives a result starting
  with `..`, so a child of the root named `..x` comes back absolute.
- `workspace.Open` resolves symlinks in its root.
- `search_files` walks through a workspace opened at the searched directory,
  with the tool set's ignore options; the candidate filter over an index's
  files is `find_references`', reached only for an index with a
  `CandidateFiles` method, which `codesearch.Index` lacks.
- `list_files` does not apply `.gitignore`; `find_files` does.
- `codesearch` indexes the walk's files within its own rules: regular files
  of at most 1 MiB with no NUL in their first 8 KiB, up to
  `Options.MaxFiles`. On Windows, where zoekt does not build,
  `codesearch.New` returns a nil index and `ErrUnsupported`.

**Code mode.**

- Starlark's builtins are one process-wide table, so scripts also see `any`,
  `all`, `chr`, `ord`, `reversed`, `tuple`, `fail`, `hash`, `bytes` and
  `set`. None reaches the host.
- A bound tool dispatches through `core.CallNested`, which works only on a
  context an agent's batch or a `Runner` prepared for a tool declaring
  `ReachableTools`; anywhere else the first call fails naming
  `core.ErrNoNestedCaller`.
- A bound tool's name must be an ASCII Starlark identifier that is not a
  builtin or one of `parallel`, `call`, `is_error`, `main` and `result`, so an
  MCP tool whose name contains `-` cannot be bound. A parameter name that is
  not an identifier is declared as written.
- A list or dict that contains itself is refused as an argument or return
  value. Return values are JSON numbers (`int64` for an int).
- Output past `MaxOutputBytes` both stops the script
  (`output_limit_exceeded`) and is truncated: the result text is the head, an
  elision marker naming the spill file, and the tail.
- Starlark resolves every name before running, so a script naming something
  undefined fails before its first call, as `runtime_error` with the line.
- A terminate vote drops its batch from the ledger: `core.CallNested` returns
  `ErrTerminated` and no results, so calls of that batch that ran are on the
  event stream only. A script that finishes as its deadline passes may be
  reported as `timeout` or `aborted`.

**The MCP client.**

- `ToolResult.Text` is set on a successful call only. An `isError` result is
  `Error: "tool_error"` with the joined text as `Detail`, so the model reads
  the code.
- Output schemas are converted by `outputSchemaFrom`, not `schemaFrom`: a
  schema that cannot be marshalled or parsed, or whose root is not an object,
  is dropped with an error diagnostic; a sub-schema the converter does not
  model becomes unconstrained; a non-object root is wrapped as `{"value":
  ...}`, as `Data` wraps non-object `structuredContent`.
- An MCP tool's server is known only from its qualified name.

**Proxies.** A sandbox proxy's `Data` is decoded from the agent's JSON with
`UseNumber`, so it re-encodes to the agent's bytes; its numbers are
`json.Number` where a local tool's are Go numbers.

## Consequences

- A change to a tool's `Data` is a change to its output schema and to every
  consumer, and is made on purpose, not to match a document.
- Callers that need a lexical scan of a Go target, or a time-bounded
  classification pass, need a change to `find_references`.
