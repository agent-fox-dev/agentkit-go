# Erratum: spec 15 `sandbox_landlock_agent`

The spec was written before spec 14 made the sandbox tree a nested module,
and before the go-landlock version gokrazy/rsync selects was known. These are
the places the delivered code differs from its text, and why.

## The sandbox tree is a nested module (D-1)

The spec says "root module", "root go.mod" and "the committed snapshot
internal/policy/testdata/go-list-m-all.txt" throughout. Since spec 14's D-2
(`docs/errata/14_sandbox_rsync_sync.md`) the sandbox code is the module
`github.com/agent-fox-dev/agentkit-go/sandbox`, so:

- go-landlock is promoted in `sandbox/go.mod`, not the root `go.mod`; the
  root module's direct set and graph do not change at all.
- The snapshot of 15-REQ-8.1 is `internal/policy/testdata/sandbox-go-list-m-all.txt`,
  and its pre-spec copy is `sandbox-go-list-m-all.14.txt`.
- TS-15-40's "14's direct set plus go-landlock" is the sandbox module's
  direct set (`TestLandlockPromoted_TS15_40`, and TS-14-42's `want`).
  TS-13-65's direct-set literal is the root module's and keeps its pre-spec
  value.

## go-landlock's version is a pseudo-version (D-2)

15-REQ-8.1 asks for go-landlock "at the version already selected after 14
(PRD 11 names v0.10.1)". gokrazy/rsync v0.3.8 selects
`v0.0.0-20250303204525-1544bccde3a3`, so that is the version promoted; no
version moved. Every symbol the spec assumes exists at it: `Config` with
`V2`, `V3` and `V4`, `Restrict`, `RestrictPaths`, `BestEffort`, `RODirs`,
`RWDirs`, `ROFiles`, `RWFiles`, `PathAccess`, `FSRule.WithRefer`,
`FSRule.IgnoreIfMissing` and `syscall.LandlockGetABIVersion`, so the probe
needs no `syscall.Syscall(444, …)` fallback.
