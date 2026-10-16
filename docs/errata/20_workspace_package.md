# Erratum: spec 20 `workspace_package`

These are the places where the delivered code and its tests differ from the
spec's text, and why. In each of the first six the code keeps what the code did
before, which is what the spec's own rule asks for: the spec "does not change
what any tool, walk or containment check does" (the PRD's intent, and R4's
"exactly what `tools.Walk` visits today").

## The repository exclude file applies when the scan root is a repository root (20-REQ-4.1, TS-20-20)

20-REQ-4.1 and TS-20-20's pseudocode say that walking `"sub"` applies the
workspace's global layer and `.git/info/exclude`, and that `z.excl`, named by
the root's `.git/info/exclude`, is not visited. The engine reads
`.git/info/exclude` only for a scan root that itself contains `.git`
(`workspace/ignore.go`, `enter`, the `os.Stat(.../.git)` test at line 149), and
`Walk` roots the engine at `sub` (`workspace/walk.go:51`). Walking a
subdirectory of a repository therefore applies the global layer and `sub`'s own
ignore files, not the root's `.gitignore` or the root's exclude file, and
`sub/z.excl` is visited.

`tools.Walk` did exactly this, and 20-REQ-4.1's contract line says the visited
set "equals what tools.Walk visited before this change". The tests assert it
(`TestWalkRootsTheEngineAtSub_TS20_20`, `workspace/walk_test.go:52`): their
expected sets are the ones the old walk produced for the same tree, and a
differential test run while the walk moved (1118 walks over 300 generated
trees, hidden entries on and off) found no difference.

## An unreadable directory is reported once, its contents never (20-REQ-4.3, TS-20-22)

The criterion says an unreadable entry "yields no fn call for it". `Walk` skips
the contents of an unreadable directory without error (`workspace/walk.go:89`),
but `filepath.WalkDir` calls the callback for the directory entry before it
tries to read it, so `fn` is called once for the directory itself, as it was
for `tools.Walk` (`TestWalkMissingRootAndUnreadable_TS0151` in
`workspace/walk_legacy_test.go`). TS-20-22 checks what the code delivers: no
entry beneath the unreadable directory is visited and `Walk` returns nil
(`workspace/walk_test.go:134`).

## `codesearch.New` on Windows returns a nil index and `ErrUnsupported` (20-REQ-9.1)

The criterion's contract line says `New` returns `(tools.Index, nil)` for a
non-nil workspace "on both `index.go` and `stub_windows.go` builds". The
Windows stub returns `(nil, ErrUnsupported)` (`codesearch/stub_windows.go:68`)
because zoekt does not build there, as before, and as `codesearch/README.md`
tells an embedder to expect. The criterion's other half, the signature, holds
on both builds: `TestEmbedderCompilesOnEveryTarget` vets the embedder program
(which calls `codesearch.New(ws, codesearch.Options{})`) for Linux, macOS and
Windows, and `GOOS=windows go vet ./...` is clean in `codesearch`.

## `Rel` keeps its `..` prefix test (20-REQ-2.6)

`Rel(abs)` returns `abs` unchanged when `filepath.Rel` gives a result that
begins with `..`, so a child of the root whose name begins with two dots (a
file called `..x`) is returned in its absolute form
(`workspace/path.go:215`). The test is the one `tools.Rel` had, copied with the
rest of the containment code; 20-REQ-2 says containment "moves unchanged".

## The index equals the walk set for the files the index accepts (20-REQ-9.3, TS-20-54)

"For any tree, index exactly the set of files that `ws.Walk` visits" is
bounded by the index's own rules, which are unchanged: it skips anything that
is not a regular file, files over 1 MiB, files with a NUL byte in their first
8 KiB, files zoekt does not index, and stops at `Options.MaxFiles`
(`codesearch/index.go`, `maxFileSize`, `binarySniffSize`, `doBuild`). The
property test generates trees of small text files, where the two sets are
equal (`codesearch/workspace_walk_test.go:92`).

## `search_files` walks through its own workspace, and the candidate filter is `find_references`'s (20-REQ-6.7, TS-20-39, 20-PATH-2)

R7 and decision 11 keep `Search`, `SearchIn` and `CountCandidates` as
root-path functions that "walk `root` through a workspace opened at `root`".
`search_files` calls `SearchIn(ctx, root, p, f.ws.Ignore())` (`tools/search.go`,
`searchFiles`), so its walk goes through a workspace opened at the searched
directory, with the tool set's memoized ignore options: the same rules, and
still one `core.excludesFile` lookup for the whole tool set (TS-20-40), but not
`Walk` on the tool set's own `*Workspace`. The symbol table, `find_files`,
the reference engine and `tools.References` do use the tool set's workspace.

20-PATH-2 says `search_files` filters an index's candidate files with
`ws.Ignored`. `search_files` never consults an index. The filter over an
index's candidates is `findCandidateFiles` in `find_references`
(`tools/ref_scanner.go:41`), reached only when the index implements the
optional `CandidateFiles` method, which `codesearch.Index` does not. 20-REQ-6.7
names that filter correctly, and TS-20-39 exercises it with an index that has
the method; TS-20-65 checks that `search_files` and `code_search` return only
non-ignored files.

## Walks that stay outside the spec's text (decision 10 and 20-REQ-6.7)

- The Go package loader found a directory through the walk and then listed it
  itself, so a gitignored `.go` file in a visible directory was still
  type-checked and its references reported. `parseDirFiles` now skips the files
  `ws.Ignored` excludes (`tools/ref_gotypes.go:119`). This is decision 10's
  "every walk uses the workspace's rules"; it changes results only for such a
  file. Measured over this repository's 61 Go packages, the per-file check adds
  about 20 ms to a reference pass of about 220 ms.
- `workspace.Open` resolves symlinks in its root, so `tools.Search`,
  `SearchIn` and `CountCandidates` called with a root that is a symlink to a
  directory now descend into it; the old walk, which ran `filepath.WalkDir` on
  the link itself, visited nothing. The tools always pass a resolved root, so
  none of their behaviour changes.

## Exported-API golden and hash pins were retired in task 5, and again in task 9 (20-REQ-10.2, TS-20-61)

The spec retires the `goldenSHA13` pins and regenerates the golden in task 10.
Task 5 adds `tools.For` and `tools.References`, which TS-13-67 and the pins
reject, and its `done_when` is that `make test` passes, so the pins were
removed and the golden regenerated there, and again for the deletions of task
9; task 10 added `workspace` to `apiPackages` and regenerated it a last time.
TS-20-61 therefore passed on its first run. `docs/errata/13_sandbox_core.md`
records the invariant's retirement.

## A few tests carry less than the spec's every bullet

- TS-20-4 skips on macOS, where `os.Getwd` still succeeds after the working
  directory is removed (the spec allows the skip); it runs on Linux.
- TS-20-33's last bullet, that the Anthropic golden request is still produced,
  is carried by `TestSmokeOutputSchemaToolsKeepGoldens_TS06_32`, which now builds
  its tools with `tools.For`.
- TS-20-52's last bullet, that the Windows stub compiles with the same `New`
  signature, is carried by `TestEmbedderCompilesOnEveryTarget`.
- TS-20-56's rsync-rules bullet is carried by the existing TS-14-9 test, which
  shows the agent's plan ignores the host's global excludes.
- TS-20-66 runs the local provider, which runs the agent in the test's process
  over pipes with its real tool set; it does not start a separate agent binary.
- TS-20-7 is a property test that is only a race test under `go test -race`,
  which is how it was run; `make test` does not pass `-race`.

## The order of the tasks (D-6, D-11)

`workspace/hidden.go` (`isHidden`) was created in task 3, where `Walk` first
calls it, instead of task 2: unused in task 2 it would have failed the
`unused` linter at that task's gate. TS-19-64 requires the regenerated golden to
be committed before it passes, so the gates of tasks 5, 9 and 10 ran on the
committed tree.
