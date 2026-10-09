# Erratum: spec 05 `find_references`

Where the delivered `find_references` differs from
`.specs/05_find_references`, and why.

## 05-REQ-10.1: the root module is no longer dependency-free or cgo-free

**Spec.** Root `go.mod` has zero `require` directives, and
`internal/policy`'s `TestNoCgoOutsideStdlib` and `TestCgoProbeIsArmed` pass
(TS-05-51).

**Code.** PRD 09 (`docs/prd/09-replace-hand-rolled-code-with-libraries.md`,
decisions D1 and D2) lifted both rules after this spec was written. Root
`go.mod` now requires tree-sitter, doublestar, go-toml and others. cgo is
allowed in files constrained by `//go:build cgo`, provided each has a pure-Go
fallback. `internal/policy/crosstarget_test.go` replaced the two named tests
with `TestCrossTargetBuildAndVet` (a `CGO_ENABLED=0` build and vet for
linux/amd64, linux/arm64, darwin/arm64 and windows/amd64) and
`TestHostCgoBuildAndVet`.

**Delivered.** `find_references` itself adds no dependency: Go resolution uses
only `go/parser`, `go/types`, `go/ast` and `go/token`. TS-05-51
(`tools/wiring_test.go`, `TestPolicyAndCrossTarget_TS05_51`) asserts that the
tool is registered and that `./tools/...` builds with `CGO_ENABLED=0` on all
four targets. It does not assert an empty `go.mod`.

## 05-REQ-10.4: `docs/GAPS.md` and the README's "What is not built" are gone

**Spec.** Move "Symbol references and callers" from Deferred to Fixed in
`docs/GAPS.md`, and remove "references and callers" from the README's "What
is not built".

**Code.** Both documents were removed in `b98d55b` ("removed some of the
docs"). They were not recreated. The README's package table lists
`find_references`, `docs/architecture.md` describes the tool, its cache and
its bounds, and `docs/api.md` documents `Workspace.References` and its types.

## TS-05-54 is not written as a test

**Spec.** TS-05-54 asserts the content of `docs/architecture.md`,
`docs/api.md`, `README.md` and `docs/GAPS.md`.

**Code.** `.specs/steering.md` forbids tests of the presence or content of
documentation. The documentation was reviewed against the code instead.

## Python hits are `lexical` only when built with cgo

**Spec.** In files the outline package covers, identifier matches outside
comments and strings are `lexical` (05-REQ-4.3, TS-05-56).

**Code.** Comment and string spans come from tree-sitter
(`outline.CommentAndStringSpans`), which needs cgo. Without cgo,
`outline/treesitter_nocgo.go` reports no classification, so
`scanContentForMatches` (`tools/ref_scanner.go`) labels every non-Go hit
`text`, and non-Go outlines are empty, so those sites are attributed to
`<file>`. TS-05-56 asserts `lexical` and function attribution when a Python
outline backend is present, and `text` otherwise.

## `Workspace.References` uses the default bounds and no cache

**Spec.** `Workspace.References` returns results identical to
`find_references` (05-REQ-9.2), and both are bounded by
`SymbolOptions.MaxFiles` and `MaxDuration` (05-REQ-6.4).

**Code.** A `Workspace` carries no `SymbolOptions` and no tool set, so
`Workspace.References` (`tools/references.go`) bounds its pass with the
defaults (50 000 files, 2 s) and type-checks from disk on every call.
`find_references` uses the tool set's `SymbolOptions` and its reference cache.
The results are identical whenever the tool set uses the default bounds and
neither pass reaches them (TS-05-46, TS-05-58).
