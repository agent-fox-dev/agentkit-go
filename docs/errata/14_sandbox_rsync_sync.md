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
