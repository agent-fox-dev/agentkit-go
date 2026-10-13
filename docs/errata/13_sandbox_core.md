# Erratum: spec 13 `sandbox_core`

Where the delivered change differs from `.specs/13_sandbox_core`, and why.

## 13-REQ-10.6: what TS-12-14's budget counts

The spec keeps the sandbox in the root module and counts the generated
`sandboxpb` code toward TS-12-14's 20,000-line budget (design decisions 4 and
5), on the premise that "this scope fits under the budget". It does not. When
the work started, the root module counted 17,815 lines. The spec then adds
877 lines of generated protocol code, moves the spec 06 conformance table
(285 lines) out of a `_test.go` file into a non-test helper package, and adds
test-only helper packages and an example, all before the sandbox code itself.
The survey's estimate came to 600–900 lines over.

The decision, taken with the project owner before any code was written, was
to measure what the budget is for — hand-maintained library code — rather
than raise the number or split the module:

- `core/size_test.go`'s `budgetLines` is what TS-12-14 now holds to 20,000.
  It is `codeLines` over the module without two kinds of file:
  - generated files, recognised by Go's `// Code generated ... DO NOT EDIT.`
    marker before the package clause;
  - packages under an `internal/` directory that no non-test Go file imports
    (test helpers that Go forces out of `_test.go` files because more than
    one package's tests use them).
- `codeLines` itself is unchanged and still counts everything, so TS-13-66
  can show that `sandboxpb` is in the root module and counted by it.
- Core's own 1,600-line budget is unchanged.

`internal/testkit`, which only tests import, is now outside the budget too.
