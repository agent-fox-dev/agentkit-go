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

## TS-13-16: ignore layers are observed through `find_files`

TS-13-16 expects `list_files` to hide a file the workspace's `.gitignore`
lists. `list_files` never applies `.gitignore` — its description says "Does
not apply .gitignore." (`tools/tools.go`, `listFiles`) — so no correct agent
passes that check. The test observes both ignore layers through `find_files`,
which applies them: a file listed only in the global excludes is found
(the agent uses `tools.NoGlobalExcludes()`), and one listed in the
workspace's `.gitignore` is not. The test also points `GIT_CONFIG_GLOBAL` at
a config naming the planted global excludes file, because git's own
`core.excludesFile` takes precedence over `XDG_CONFIG_HOME` and would make
the check depend on the developer's configuration.

## 13-REQ-10.4: one case of the shared conformance table compares by value

The spec 06 cases moved into `internal/toolconformance` unchanged but for
one assertion. TS-06-33 checked `res.Data["exit_code"] != 2`, which holds for
a local tool's `int` but not for a proxy's `json.Number` — proxy `Data` is
decoded from the agent's JSON with `UseNumber` (13-REQ-7.1), so that it
re-encodes to the agent's bytes. The case now compares
`fmt.Sprint(res.Data["exit_code"])` with `"2"`: the same value for both, and
the outcome against `tools.All` is unchanged. Each case group also carries
its own workspace tree, so the files TS-06-22 and TS-06-29 used to add after
the tools were built (`empty/`, the mode-0 `locked.go`) are in the tree the
builder receives — for proxies, in the tar synced into the sandbox.
