# Report a file's syntax errors with its outline

## Intent

A program that outlines a source file with `outline` also learns whether
the file parsed cleanly and, if not, where it did not. A caller that edits
a file by its declarations can then refuse an edit that leaves the file
unparseable, and can tell a declaration's reported range it may trust from
one taken out of a damaged tree, without a second parser and without a
grammar list of its own.

## Goals

1. One call to `outline.Outline` returns, with the declarations, whether
   the file was parsed and the position of every syntax error found, up to
   a limit.
2. A file without syntax errors, in each of the fifteen languages, is
   reported as parsed with no errors.
3. A file with a syntax error, in each of the fifteen languages, is
   reported as parsed with at least one error whose line is the line of
   the fault or of the first token after it.
4. A file that was not parsed says why: no backend for its language, over
   the size limit, or binary. "Not parsed" and "parsed, no errors" cannot
   be confused.
5. `outline.CommentAndStringSpans` answers for Go files, with and without
   cgo.
6. Every program that builds against `outline` today builds unchanged, and
   the declarations `Outline` and `OutlineMany` return for any input are
   the same as before.

## Non-goals

- Error messages that explain the fault, or suggested fixes. An error is a
  position and a kind.
- Semantic errors: unresolved names, types, unused imports. Only what the
  parser rejects.
- Showing syntax errors in the output of the `file_outline` and
  `find_symbol` tools, or storing them in a `codesearch` index. Their
  output is unchanged.
- A declaration's leading comment or annotations in its line range.
  `StartLine` stays the line of the declaration's name.
- Byte ranges of declarations.
- New languages, or syntax checking for languages `outline` does not
  outline.
- Tree-sitter without cgo. Without cgo the verdict exists for Go only, as
  the outline does.

## Background

`outline` (`outline/outline.go`) returns a `File` for one source file:
`Path`, `Lang`, `Backend` and `Decls`. Go is parsed with `go/parser`
(`outline/goast.go`), fourteen other languages with tree-sitter when built
with cgo (`outline/treesitter.go`), and `BackendNone` covers everything
else: an unknown extension, a file over `Options.MaxFileBytes` (1 MiB by
default), a file with a NUL byte in its first 8 KiB, a language with no
grammar in this build, and a Go file for which the parser returned no tree
at all.

Both backends parse a file with syntax errors and outline what they
recovered. `outlineGo` discards the parser's error ("Even if err != nil
(syntax errors), we use the partial AST"), and the tree-sitter backend
never looks at the tree's error nodes. So a caller cannot tell a clean file
from a damaged one, and cannot tell why a file came back with no backend.

`CommentAndStringSpans` (`outline/treesitter.go`) returns the byte ranges
of comments and string literals from the tree-sitter grammar. Go has no
entry in the grammar table, so for a Go file it answers `ok == false` in
every build; without cgo it answers so for every file
(`outline/treesitter_nocgo.go`). `tools/ref_scanner.go` is its one caller
in this repository.

Parley, which serves outlines from its hub, is the first consumer that
needs both: it plans to replace a declaration of a file by name and refuse
a result that does not parse, and to find mentions of a name outside
comments and strings, mostly in Go.

## Requirements

### The parse verdict of a file

`outline.File` gains three exported fields, all new: `Parsed`,
`NotParsed` and `SyntaxErrors`, with `SyntaxErrorsTruncated`. `Outline` and
`OutlineMany` fill them for every file they return.

- **Parsed.** True when a backend parsed the file's bytes, whether or not
  it found errors. Then `Backend` is not `BackendNone`.
- **NotParsed.** Empty when `Parsed` is true. Otherwise one of a closed
  set of reasons, exported as constants:
  - `unsupported`: the extension is unknown, or this build has no backend
    for the language (any language but Go without cgo);
  - `too_large`: the file is over `Options.MaxFileBytes`;
  - `binary`: a NUL byte in the first 8 KiB;
  - `failed`: the backend returned no tree (a Go file the parser gave no
    AST for, a grammar that could not be loaded).

  In every one of these cases `Backend` is `BackendNone` and `Decls` is
  empty, as today.
- **SyntaxErrors.** The errors the parse found, in position order; empty
  and never nil when there are none or the file was not parsed. Each is a
  new exported type with `Line` (1-based), `Column` (1-based, in bytes),
  and `Kind`, one of two constants: `unexpected` (text the grammar could
  not place) and `missing` (a token the grammar needed and did not find,
  at the position where it was expected).
- **The limit.** At most `Options.MaxSyntaxErrors` errors are returned, a
  new option whose zero value means 20. When the parse found more,
  `SyntaxErrorsTruncated` is true and the first ones in position order are
  kept. A negative value is treated as zero.

What counts as an error, per backend:

- **Go.** Every error `go/parser` reports for the file with all errors
  requested, at the position the parser gives. All are `unexpected`,
  except that an error whose message starts with `expected` and names a
  single token is `missing`. An empty `.go` file has one error, at line 1,
  because the language requires a package clause.
- **Tree-sitter.** Each error node of the tree that has no error node
  among its ancestors is one `unexpected` error at the node's start. Each
  missing node is one `missing` error at its position. A tree without
  either has no errors.

What does not change:

- The declarations of any file, clean or damaged: same entries, same
  order, same line ranges as before this change.
- The language and backend decisions, the `.h` rule for C++ and the `.tsx`
  rule. The verdict comes from the same parse as the declarations, so a
  file is never outlined by one backend and checked by another.
- A cancelled context still returns `ctx.Err()` and no `File`.
- An unreadable file is still an error from `Outline`, and a `File` with
  `BackendNone` from `OutlineMany`, there with `NotParsed` `failed`.

Constraint: a grammar can lag its language. A file that uses syntax newer
than the grammar in this build is reported with errors although the
language accepts it. The package's documentation says so, and a caller
that refuses files on this verdict is told to offer a way past it.

### Comment and string spans for Go

`CommentAndStringSpans` answers for a Go file in every build, cgo or not:

- the spans are the byte ranges `[start, end)` of every comment (line and
  block), every string literal (interpreted and raw) and every rune
  literal, in order and not overlapping;
- `ok` is true for any Go file, with or without syntax errors: the spans
  come from the tokens, and an unterminated literal or comment extends to
  the end of the file;
- an empty or nil `src` answers no spans and `ok == true`.

For every other language the function behaves as today: from the
tree-sitter grammar with cgo, `ok == false` without it or without a
grammar.

`tools/ref_scanner.go` calls it for every file, so reference search over
Go files stops counting names inside comments and strings. That is a
change in what the `find_references` tool and `tools.References` return
for Go; it is the behaviour they already have for the other languages.

### Documentation

`docs/api.md` describes the new fields, the reasons, the limit, the
grammar constraint and Go's spans; `make doc` regenerates
`docs/generated/`.

### End-to-end flows

1. **A clean file.** A program calls `outline.Outline` with the bytes of a
   Python file. The `File` has `Parsed` true, `Backend` tree-sitter, its
   declarations, and no syntax errors.
2. **A missing brace.** The program removes the closing brace of a Go
   function and calls again. The `File` has `Parsed` true, `Backend`
   `go/ast`, the declarations the parser recovered, and at least one
   syntax error at or after the function's last line.
3. **Not parsed.** The program passes a 2 MiB JavaScript file. The `File`
   has `Parsed` false, `NotParsed` `too_large`, no declarations and no
   errors.
4. **Without cgo.** The same program built with `CGO_ENABLED=0` gets
   `NotParsed` `unsupported` for the Python file and the full verdict for
   the Go file.
5. **A name in a comment.** `find_references` for `Greet` over a Go
   workspace no longer lists the line `// Greet is deprecated`.

## Design Decisions

1. **The verdict is part of the outline, not a second function.** Every
   caller that needs the verdict also needs the declarations, and one
   parse cannot disagree with itself. It costs the outline nothing: both
   backends already have the information and drop it.
2. **A reason for "not parsed".** Today four different causes look the
   same (`BackendNone`), and a caller that refuses or accepts an edit
   needs to know which it has.
3. **Position and kind, no message.** Tree-sitter has no message to give,
   and a message for Go only would make callers depend on one backend.
4. **The outermost error node only**, so one fault is one error and not
   one per nested fragment.
5. **A default limit of 20.** A badly broken file can produce thousands of
   error nodes; a caller wants the first few.
6. **Go's spans come from its tokens, not from a tree-sitter grammar for
   Go.** It works without cgo and adds no dependency.
7. **The change to Go reference results is accepted** as the correction
   of a difference between Go and the other languages, not kept behind an
   option.
8. **The leading comment stays outside the line range.** Changing
   `StartLine` would change every stored outline and every tool answer;
   a consumer that needs the comment asks for it in a later PRD.

## Dependencies

- The `outline` package and `tools/ref_scanner.go`, as they are at
  v0.10.0. Their packages are archived; no active package is changed.
- ADR 10 (`docs/adr/10-keep-the-runner-and-the-output-buffer-free-of-cgo-and-providers.md`):
  unaffected. `outline` keeps needing cgo for the fourteen languages, and
  nothing new is imported by the runner or the output buffer.
- Parley PRD 21 (`docs/prd/21_declaration_reads_and_writes.md` in
  `agent-fox-dev/parley`) is blocked on the parse verdict; Parley PRD 22
  (`docs/prd/22_declaration_awareness.md`) uses Go's spans.

## Verified External API

Read in the module cache at `github.com/agent-fox-dev/agentkit-go@v0.10.0`
and in the Go standard library:

- `go/parser.ParseFile(fset *token.FileSet, filename string, src any, mode parser.Mode) (*ast.File, error)`;
  the mode `parser.AllErrors` reports every error instead of the first
  ten; the error is a `go/scanner.ErrorList`, whose entries have
  `Pos token.Position` (with `Line` and `Column`) and `Msg string`.
  `outline` calls it today with `parser.SkipObjectResolution` only.
- `go/scanner.Scanner`: `Init(file *token.File, src []byte, err scanner.ErrorHandler, mode scanner.Mode)`
  and `Scan() (pos token.Pos, tok token.Token, lit string)`; the mode
  `scanner.ScanComments` returns comments as `token.COMMENT`; string and
  rune literals are `token.STRING` and `token.CHAR`.
- `github.com/tree-sitter/go-tree-sitter` v0.25.0, as `outline` uses it:
  `(*Tree).RootNode()`, `(*Tree).Walk()`, `(Node).StartPosition()` with
  `Row` and `Column`, `(Node).Kind()`, `(Node).IsNamed()`; and, read in
  its `node.go`, `(*Node).HasError() bool`, `(*Node).IsError() bool` and
  `(*Node).IsMissing() bool`.

**Unverified**: that a missing node's start position is where the token
was expected. **Unverified** for Go: that every `expected <token>`
message of `go/parser` names a single token; where it does not, the error
is `unexpected`.

## Glossary

- **Outline**: the declarations of one source file, as `outline.Outline`
  returns them.
- **Backend**: what parsed a file: `go/ast`, `tree-sitter`, or none.
- **Parse verdict**: whether a file was parsed, why not if not, and the
  syntax errors the parse found.
- **Error node, missing node**: tree-sitter's two marks in a syntax tree
  for text it could not place and for a token it had to assume.
- **Span**: a byte range `[start, end)` of a comment or a string literal.
