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

## Spec 20: the frozen exported-API invariant is retired

PRD 12 (`docs/prd/12-simplify-the-embedder-contract.md`) breaks the `tools` API
on purpose, and spec 20 (`20_workspace_package`) does it. `tools.Workspace`,
`NewWorkspace`, `Walk`, `WalkOptions`, `IgnoreOptions`, `NoGlobalExcludes`,
`Normalize`, the three path sentinels, `All`, `(*Workspace).References` and
`Options.Workspace` and `Options.Ignore` leave `tools`; `tools.For`,
`tools.References` and the new package `workspace` arrive; and `SearchIn`'s
ignore parameter changes type. Spec 13's invariant that the exported APIs of
agentkit, core, tools and codemode do not change (13-REQ-10.7), and the hash
pins that held `testdata/exported-api.golden` to spec 13's version
(`goldenSHA13` in TS-14-44, TS-15-42 and TS-17-61), are retired. TS-17-61 keeps
its check of `guard`'s exported API.

The golden stays as a change detector, over the list `apiPackages` in
`internal/policy/layout_test.go`. This entry says what that list covers, who
regenerated the golden, how its four tests relate and how it may change.
Spec 24 (`24_embedder_contract_docs`) completed it.

**What the golden covers.** One `== <pkg>` section for each entry of
`apiPackages`, in this order, each the package's exported declarations as
`exportedAPI` prints them:

| Section | Why it is there | Added by |
|---|---|---|
| `.`, `core`, `tools`, `codemode` | the original contract spec 13 froze | spec 13 |
| `workspace` | PRD 12's new package | spec 20 |
| `guard` | PRD 12 added `Allow` | spec 24 |
| `provider/faux` | PRD 12 added `ToolCall` and `Text` | spec 24 |
| `sandbox` | PRD 12 added `Open` and changed the type of `LocalDir.Ignore` | spec 24 |
| `codesearch` | PRD 12 made `New` take a `*workspace.Workspace` and removed `Options.Ignore` | spec 24 |
| `mcpserve` | PRD 12's new module | spec 24 |

The five packages spec 24 added are the ones PRD 12 changed or added that the
golden did not cover. They were appended after the existing sections, so no
existing line moved. Packages PRD 12 did not change stay out.

**Who regenerated it.** Four specs, each for the surface it changed:

- Spec 20 added `tools.For` and `tools.References`, removed the old `tools`
  identifiers and the `SearchIn` ignore parameter's old type, and added the
  `workspace` section.
- Spec 21 reshaped `Config` (`Toolset` replaces `Tools`, `Policy`, `Guard` and
  `After`) and added `Toolset`, `NewRunner`, `Runner` with `Call` and `Batch`,
  and `Agent.Runner`, in two regenerations.
- Spec 22 added `core.Named`.
- Spec 24 added the five sections above, in a single regeneration. The
  existing five sections are byte-identical to what spec 23 left, because spec
  23 changed no surface the golden covers.

PRD 12's acceptance criterion 12 asked for one regeneration; the work was
split into five specs and the golden regenerated in four of them
(`docs/errata/24_embedder_contract_docs.md`).

**How its tests relate.** TS-13-67, TS-14-44, TS-15-42 and TS-17-61 compare the
same golden over the same `apiPackages`, so each widens when the list does.
TS-13-67 writes it (with `AGENTKIT_UPDATE_API_GOLDEN=1`) and, when it differs,
fails listing the lines the code gained and lost; a directory in `apiPackages`
that does not exist fails it, naming the path, and does not skip. Spec 20
retired the `goldenSHA13` pins that held the other three to spec 13's version.
TS-17-61 also keeps a list of its own, `preSpec17Guard` (spec 22 added `Allow`
to it), which it compares with `guard`'s exported lines in addition to the
golden.

**The rule from now on.** A change to the exported API of any package in
`apiPackages` regenerates the golden in the same change:

```
AGENTKIT_UPDATE_API_GOLDEN=1 go test ./internal/policy -run TS13_67
```

and commits it, because TS-19-64 fails while `internal/policy/testdata` has an
uncommitted change. The diff of the golden is the review of the surface, and
the spec or change that makes it says why in its own record. A package joins
the golden by being appended to `apiPackages`, so the sections already there do
not move.

**The `codesearch` section lists the Windows stub.** `exportedAPI` parses every
non-test file whatever its build tags, so the section holds the declarations of
`stub_windows.go` (`Options`, `New`, and the `*Index` methods with an unnamed
receiver) beside the real ones (receiver `idx`). That is expected: the doubled
lines make drift between the stub and the real index visible, and
`exportedAPI` is not changed to hide it.
