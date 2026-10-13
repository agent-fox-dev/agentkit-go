# Erratum: spec 14 `sandbox_rsync_sync`

Where the delivered change differs from `.specs/14_sandbox_rsync_sync`, and
why. Each decision was taken with the project owner before the code it
affects was written.

## 14-REQ-9.4: the sandbox tree is a nested module (D-2)

After spec 13 the root module counted 19,864 of TS-12-14's 20,000 lines
(under spec 13's `budgetLines`), and this spec adds about 900 lines of
production code. 14-REQ-9.4 prescribes spec 13's design decision 5 for that
case, and it was applied before task 1:

- `sandbox/` has its own `go.mod` (`github.com/agent-fox-dev/agentkit-go/sandbox`),
  requiring the root module through `replace ../`, as `codesearch` does.
  Import paths are unchanged. grpc and protobuf are its direct requirements;
  in the root module they are indirect again, and the root's `go list -m all`
  equals spec 13's snapshot.
- `examples/sandbox/` has its own `go.mod`, so it keeps the path both specs
  name while importing the sandbox module the root does not require.
- The Makefile's `test`, `vet`, `lint`, `tidy` and `doc` targets loop over
  every module, and the cross-target and host-cgo gates build and vet the root
  and both sandbox modules.
- `buildver` reports the sandbox module's version, the module the agent is
  now built from.

Spec 13's tests that encoded one module change with it:

- TS-13-2 builds and vets the sandbox module in its own directory.
- TS-13-65 checks grpc and protobuf in `sandbox/go.mod`, the root's direct set
  against the pre-spec-13 set, and the sandbox module's direct set against the
  same allowlist.
- TS-13-66 now asserts the opposite of what it asserted: `sandbox/go.mod`
  exists, the root counter never visits the tree, and the root holds the
  budget. 13-REQ-10.6's "no go.mod under sandbox/" no longer holds.
- TS-13-67's layout walk skips nested modules, as it always did, so the
  sandbox tree is outside it; the exported-API golden is unchanged.

## The rsync library cannot filter or protect: the transfer is redesigned (D-3)

The spec's external API section marks `gokrazy/rsync`'s filter and delete
behaviour as unverified. Reading v0.3.8 showed it cannot carry the spec's
design (14-REQ-3.6, 4.2, 5.3; design decisions 1–3):

- A rule containing `*`, `?` or `[`, escaped or not, makes the sender panic:
  `wildcard filter rules not yet implemented` (`internal/sender/exclude.go`,
  `filterRule.matches`). In `local` that panic would take down the
  embedder's process.
- Rule matching is string equality: the whole relative name when the pattern
  contains `/`, else the base name. There is no anchoring, and file-list names
  carry no leading `/`, so the spec's `/rule` form never matches.
- On a match the sender returns `filepath.SkipDir` (`internal/sender/flist.go`),
  which for a file skips the rest of its directory: its later siblings are
  silently not sent.
- The receiver's `--delete` removes every path missing from the sender's file
  list and consults no filter (`internal/receiver/do.go`, `deleteFiles`; the
  server discards the exclusion list it reads). Export with `Delete` would
  wipe `dst/.git` and a host `.env`.
- `rsyncclient`, as receiver, applies Landlock to the whole process unless
  built with `rsyncclient.DontRestrict()` (`internal/maincmd/clientmaincmd.go`);
  the spec only knows the server's option.
- The receiver sets the process umask to 0 and back during a transfer
  (`internal/receiver/permissionsumask.go`), and `Client.Run` ignores its
  context.

What the delivered code does instead, as decided with the project owner:

- **The sender always serves a filtered tree.** The sending side runs an
  `rsyncd` server whose one module is a read-only `fs.FS` over its tree in
  which every excluded path does not exist; the receiving side runs an
  `rsyncclient` receiver writing through the library's `os.Root`. On Sync the
  host serves `LocalDir.Path` and the agent's client receives into the
  workspace; on Export the agent serves the export root and the host's client
  receives into the destination. This swaps the rsync roles 14-REQ-4.2 gives
  Sync (the host is not an `rsyncclient` sender).
- **The library never sees a rule or `--delete`.** Rules are workspace- or
  destination-relative paths matched exactly, as the path or an ancestor of
  it, plus the `.git` rule, which matches a `.git` path component at any
  depth; wildcard characters in names are literal. `Combine` returns the
  sorted, deduplicated union in that raw form, with no anchoring or escaping.
- **The protocol carries what the library cannot.** The `args` frame keeps its
  form — the agent's side in rsync server-argument syntax, with the sender
  flag exactly for EXPORT and `--delete` as the declared intent — and gains
  `rules` (the combined rules) and, on SYNC with delete, `present` (the host
  sender's transfer set). `RsyncDone` gains `present` for EXPORT with delete.
  These are additions to 14-REQ-2.1's frame contents; the oneof members are
  unchanged.
- **Deletion is AgentKit's own code** on the receiving side: after the
  transfer it removes the entries no combined rule covers and the sender's set
  lacks, through an `os.Root`, never following a symlink.
- **Both constructors restrict nothing.** `rsyncd.NewServer` and
  `rsyncclient.New` are each called in exactly one function of
  `sandbox/internal/rsyncx`, always with `DontRestrict()`; the AST test checks
  both.

## 14-REQ-9.1: the version moves D-1 accepted

`gokrazy/rsync` v0.3.1 to v0.3.8 all require `golang.org/x/crypto` v0.46.0,
above the v0.40.0 already in the graph, so 14-REQ-9.1's "change the version
of no module already in go.mod" cannot hold with the version the spec pins.
The project owner accepted the moves (D-1). Because of D-2 they happen only
in the sandbox module's graph: `x/crypto` v0.40.0→v0.46.0, `x/net`
v0.41.0→v0.47.0, `x/text` v0.27.0→v0.32.0, `x/mod` v0.25.0→v0.30.0,
`x/tools` v0.34.0→v0.39.0 and `BurntSushi/toml` v0.3.1→v1.6.0. All are
indirect. TS-14-42 compares the sandbox module's graph with its pre-rsync
snapshot (`internal/policy/testdata/sandbox-go-list-m-all.13.txt`): every
line is unchanged except these six, and the root module's graph still equals
spec 13's snapshot.

## 14-REQ-3.1 and 14-REQ-5.3: a subdirectory export honours the workspace's ignore files

14-REQ-3.1 says each side computes its ignore rules "over its own tree", and
TS-14-27 expects `Export("logs", ...)` to leave out `logs/app.log` when the
workspace root's `.gitignore` says `*.log`. A walk rooted at the export path
does not see ignore files above it, so the agent's rules now come from a walk
of the whole workspace, re-rooted at the export path
(`rsyncx.RuleOptions.Base`, `sandbox/internal/rsyncx/rules.go`; the agent
passes `Base: a.root` in `sandbox/internal/agent/rsync.go`). An export path
that the workspace's own rules ignore was named explicitly, so it takes only
the ignore files inside it, as a single file takes none (14-REQ-3.2).
Escaping symlinks are still decided relative to the export path.

## Task 7: where the export plumbing differs from its steps

- The steps name `rsyncx.ClientArgs(ld.Delete, combined)` and
  `rsyncx.RunClient(..., clientArgs, false, dst+"/")`. Under D-3 the rules
  travel in the args frame and never reach the library, so `ClientArgs`
  takes only the delete intent and `RunClient` takes the destination
  directory; the operand (`"."`, `dir/` or a file's path) is what maps a
  directory's contents, or a file under its base name, onto it.
- TS-14-33's push into an EXPORT-mode server needs a client built with
  `WithSender`, which only `sandbox/internal/rsyncx` may construct (TS-14-36),
  so that half lives in `sandbox/internal/rsyncx/server_test.go`; the Export
  half is in `sandbox/rsync_export_test.go`.
