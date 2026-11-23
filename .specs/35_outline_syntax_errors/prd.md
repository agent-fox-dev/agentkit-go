---
spec_id: "35"
spec_name: "outline_syntax_errors"
title: "Report a file's syntax errors with its outline"
status: "active"
created_at: "2026-11-23T08:37:21Z"
intent_hash: "56491235e327221d6a8710df2b35b17551f17b01bf39ed8bb34fc0b625a90695"
contract_hash:
  requirements.json: "d31f95feb6938882e2277b34949d763c5c9431a902a243936e849fb8c263a1f5"
  tasks.json: "5e6deb6386047253ba12969881cf25a21bfcabd80bdc394140e6dc085ccf730e"
schema_version: 3
source: "docs/prd/17-report-syntax-errors-with-a-file-outline.md"
---

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
   the declarations `Outline` and `OutlineMany` return are the same as
   before, with one exception: a Go file with errors on more than ten
   lines, which had no declarations, has the ones the parser recovers
   (design decision 9).

## Non-goals

- Error messages that explain the fault, or suggested fixes. An error is a
  position and a kind.
- Semantic errors: unresolved names, types, unused imports. Only what the
  parser rejects.
- Showing syntax errors in the output of the `file_outline` and
  `find_symbol` tools, or using them in `codesearch`. Their output is
  unchanged and `codesearch` gets no code change (design decision 14).
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

- **Go.** Every position at which `go/parser` reports an error for the
  file with all errors requested: the parser can report several messages
  at one position, and they are one error (design decision 10). An error
  is `unexpected`, except that a position with a message that starts with
  `expected '` and so names one token in quotes (`expected ')'`,
  `expected ';'`) is `missing`. An empty `.go` file has one error, at
  line 1 column 1, because the language requires a package clause.
- **Tree-sitter.** Each error node of the tree that has no error node
  among its ancestors is one `unexpected` error at the node's start. Each
  missing node is one `missing` error at its position. A tree without
  either has no errors.

What does not change:

- The declarations of any file, clean or damaged: same entries, same
  order, same line ranges as before this change. The one exception is a
  Go file with errors on more than ten lines: the parser used to give up
  on it and the outline was empty; parsed with all errors requested it has
  the declarations the parser recovers (design decision 9).
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
- `ok` is true for any Go file, with or without syntax errors, and
  whatever the state of `ctx`: the spans come from the tokens. An
  unterminated raw string or block comment extends to the end of the
  file; an unterminated interpreted string or rune literal ends where its
  token ends, at the end of its line (design decision 11);
- an empty or nil `src` answers no spans and `ok == true`.

For every other language the function behaves as today: from the
tree-sitter grammar with cgo, `ok == false` without it or without a
grammar.

`tools/ref_scanner.go` calls it for every file it scans lexically, so a
lexical reference search treats a Go file as it treats the other
languages: a hit in code is `lexical`, and a hit inside a comment or a
string stays listed, as `text`. Until now every hit in a Go file was
`text`. That is a change in what the `find_references` tool and
`tools.References` return for a name no Go package of the workspace
declares; a name a Go package declares is resolved by `go/types` as
before, and a search with the `text` backend labels every hit `text` as
before (design decision 12).

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
5. **A name in a comment.** `tools.References` for `Marshal`, which no Go
   package of the workspace declares, over a workspace with a Go file
   that calls `yaml.Marshal` and has the line `// Marshal is deprecated`:
   the call is `lexical` and the comment line is `text`. Before, both
   were `text`.

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

9. **One Go parse, with all errors requested, and the declarations it
   recovers.** Settled with the user while specifying. Without that mode
   `go/parser` gives up after errors on more than ten lines and returns an
   empty tree, so such a file had no declarations; the same parse now
   reports every error and yields the declarations. Parsing twice to keep
   the empty outline was rejected: it would keep a defect and break "one
   parse cannot disagree with itself".
10. **Go errors at one position are one error.** With all errors requested
    the parser reports up to three messages at one position (an empty file:
    `expected ';'`, `expected 'IDENT'`, `expected 'package'`, all at 1:1).
    One fault is one error, as for tree-sitter (decision 4), and it is what
    makes "an empty `.go` file has one error" true. The position is
    `missing` when any of its messages names a quoted token.
11. **An unterminated interpreted string ends at its line's end.** That is
    where Go's scanner ends the token, and a string that swallowed the
    rest of the file would hide every later reference. A raw string and a
    block comment do run to the end of the file.
12. **A Go comment or string hit stays listed, as `text`.** Settled with
    the user while specifying. The other languages list such hits as
    `text` and do not drop them, and the purpose of this change is that Go
    behaves as they do (decision 7). The earlier wording, that the line is
    no longer listed, described no language.
13. **Go's spans do not consult `ctx`.** A token scan of a file under the
    size callers already bound is not worth cancelling, and "`ok` is true
    for any Go file" stays without an exception.
14. **`codesearch` is not changed.** Settled with the user while
    specifying. Its stored index encodes each `outline.File` whole, so
    `state.json` carries the new fields as a side effect; nothing in
    `codesearch` reads them, the store's format version stays 1, and an
    index stored before this change loads with the fields at their zero
    values.
15. **`failed` covers every tree-sitter error that is not the context
    ending**, as the code already treats them alike: a grammar or a query
    that does not load, a parser that refuses the language.
16. **The verdict's consumer is the embedding program.** Nothing in this
    repository reads `Parsed`, `NotParsed` or `SyntaxErrors`: the tools
    and `codesearch` are deliberately unchanged, and Parley is the first
    reader. For these fields the exported API of `outline` is where the
    result is consumed; only Go's spans have a caller here, the reference
    scanner.

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

Checked while specifying, by running both parsers:

- a tree-sitter missing node is zero bytes wide and starts where the
  token was expected (`def f(:` in Python: a missing `)` at line 1
  column 7; `int x = 1` without its semicolon in Java: a missing `;` at
  line 2 column 12), and an error node can lie inside another error node;
- `go/parser` writes a token it expected in single quotes
  (`expected ')', found '{'`) and anything else without them
  (`expected type, found ')'`, `expected operand`), which is the rule for
  `missing` above; it reports `string literal not terminated`,
  `rune literal not terminated` and `comment not terminated` at the start
  of the token;
- without `parser.AllErrors` the parser drops a second error on one line
  and stops after errors on more than ten lines, returning a tree with no
  declarations; with it, a file with fourteen such lines keeps all its
  declarations and reports fourteen errors;
- `go/scanner` removes carriage returns from the literal of a comment
  and of a raw string, so the length of the literal is not the length of
  the token in the source.

## Glossary

- **Outline**: the declarations of one source file, as `outline.Outline`
  returns them.
- **Backend**: what parsed a file: `go/ast`, `tree-sitter`, or none.
- **Parse verdict**: whether a file was parsed, why not if not, and the
  syntax errors the parse found.
- **Error node, missing node**: tree-sitter's two marks in a syntax tree
  for text it could not place and for a token it had to assume.
- **Span**: a byte range `[start, end)` of a comment or a string literal.
