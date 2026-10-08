# Erratum: find_references — earlier specs' documentation tests superseded

**Relates to:** `.specs/05_find_references/prd.md`, documentation changes
(README.md, docs/GAPS.md, docs/api.md), and the default tool set.

Spec 05 asks for edits that tests written for earlier specs had pinned to
their pre-spec-05 state. The tests are updated to the new state rather than
the edits being withheld.

## README "What is not built"

**What earlier specs said.** TS-01-70 and TS-02-59 required the section to
list references and callers as unbuilt.

**What is implemented.** Spec 05 removes that bullet. Both tests still require
the section to exist but no longer require the bullet.

## docs/GAPS.md

**What earlier specs said.** TS-02-60 required the references and callers row
to point to spec 02's Non-goals.

**What is implemented.** Spec 05 moves the row to Fixed. The test now requires
the row to cite `05_find_references`.

## docs/api.md mentions `outline`

**What earlier specs said.** TS-01-71 forbade the word `outline` in api.md,
the network API document.

**What is implemented.** Spec 05 documents `Workspace.References` there, and
its signature names `outline.Decl`. The test now forbids only documentation of
the `outline` package's own API.

## reqtool06_test.go is edited

**What earlier specs said.** TS-04-32 required `tools/reqtool06_test.go` to be
unchanged since spec 04's base commit.

**What is implemented.** `TestTheDefaultToolSetIsPlatformStable` pins the
default tool set, and spec 05 adds `find_references` to it. TS-04-32 now
guards `tools/tools_test.go` only.
