# Dependency ledger

REQ-GO-11 / REQ-GO-13, as relaxed by
[`errata/dependency_policy.md`](errata/dependency_policy.md). The policy is:
**the standard library is preferred; a third-party module is allowed when this
file says why it earns its place; cgo is never allowed.** `go.mod` is the
authority on what the root module requires. This file is documentation of the
reasons, not an allowlist the build checks, and nothing fails when the two
differ — a reviewer is expected to notice.

What *is* executable lives in `internal/policy`: `TestNoCgoOutsideStdlib` and
`TestCgoProbeIsArmed` reject a cgo dependency, `TestCrossTargetBuildAndVet`
builds the four supported targets, and `TestDependencyPolicyIsRecorded` keeps
the errata and this file linked.

The file holds bindings only. Per-cycle narrative belongs in commit messages; a
ledger that grows a section per cycle stops being read and therefore stops
binding.

## Third-party modules in the root

The root module requires no third-party module today (`go list -m all` shows
only itself). When one is added, it gets a row here: what it buys, why
hand-rolling is not credible, and the ruling that records the decision (see R6).

| Module | Buys | Why not hand-roll | Ruling |
|---|---|---|---|
| — | — | — | — |

Nested modules (`codesearch/`, `difftest/`, `examples/codesearch/`,
`examples/flatline/`) carry their own `go.mod`; they do not appear in the
root's build graph. A nested module is still the only mechanism in Go that keeps
a dependency out of the root's `go.mod`, `go.sum`, `go list -m all` and every
downstream SBOM, and remains the right home for a heavy or opt-in dependency.
Dependencies of a nested module are recorded as rulings below (R7 for zoekt in
`codesearch/`).

## Rulings

Decisions about the dependency surface are recorded once and are not
re-litigated. Re-asking a settled question is itself a failure mode
(NFR-COMPAT-07.3).

- **R1 — stdlib preferred, not enforced (amended).** The root module's original
  budget was the Go standard library alone, enforced by `allowedModules` and
  `TestNoUnapprovedModules`. That gate was removed
  ([`errata/dependency_policy.md`](errata/dependency_policy.md)): holding every
  dependency to a test-file allowlist made the codebase harder to work in than
  the property was worth. The standard library is still the default answer, and
  code that is cheap to write against it should be. A build tag or a sub-package
  still does not confine a dependency — it lands in `go.mod`, `go.sum`,
  `go list -m all` and every downstream SBOM — so a dependency that only some
  embedders want belongs in a nested module.
- **R2 — cgo is rejected regardless of any other ruling.** `TestNoCgoOutsideStdlib`
  fails on any non-stdlib package shipping cgo files, because cgo breaks the
  cross-target gate of NFR-COMPAT-06 (`TestCrossTargetBuildAndVet`:
  linux/amd64, linux/arm64, darwin/arm64, windows/amd64). The check runs with
  `CGO_ENABLED=1` and that is load-bearing: with cgo off the toolchain excludes
  cgo files by build constraint, so the probe reports zero and passes while the
  dependency is present. `TestCgoProbeIsArmed` guards the guard.
- **R3 — TOML is hand-rolled.** `internal/toml` reads the subset that manifests
  and `config.toml` use (strings, booleans, integers, string arrays, tables,
  arrays of tables) and reports what it does not read as a diagnostic that skips
  the key (REQ-SKILL-10). A full TOML library would buy floats, dates, inline
  tables and multi-line strings, none of which a manifest needs, at the cost of
  a new module in the root. The reader stays as it is; the reason to keep it is
  the diagnostic behaviour above, not a ban on modules. Do not re-open on the
  strength of a manifest wanting a float.
- **R4 — JSON at trust boundaries is hand-rolled.** `wire` parses untrusted
  bytes with bounds applied before allocation, duplicate-key rejection and
  literal preservation (REQ-SEC-11/12). `encoding/json` accepts duplicate keys
  silently and reads a declared length before it bounds it; no third-party
  parser was evaluated because the requirement is the bounds, not the speed. A
  parser that provides those properties would be weighed on them.
- **R5 — vendor SDKs stay out of the root.** The provider packages speak each
  API over `net/http` directly. A vendor SDK would pin the root to that
  vendor's release cadence and transitive tree, and NFR-TEST-06's reference
  bodies — the one place a vendor SDK is wanted — belong in the nested
  `difftest/` module for exactly that reason.
- **R6 — a new dependency is justified in review.** When a module is added to
  the root, the change answers three questions, in the commit and in a row of
  the table above: what it buys; whether hand-rolling is credible; and where the
  ruling is recorded (a new R-number here). Also check that it ships no cgo
  (R2), that it does not pull a server stack or a large transitive graph the
  root's embedders would not expect, and whether a nested module would serve
  better. No test checks this; the review does.
- **R7 — zoekt is confined to the `codesearch/` nested module, pinned.**
  `github.com/sourcegraph/zoekt` (pinned at
  `v0.0.0-20260911061844-153817f643cd`) provides trigram-based code search with
  boolean queries, ranking and symbol awareness. Hand-rolling a trigram index
  with ranking, a boolean query parser, chunk-match extraction and per-shard
  search is not credible — zoekt is a mature, battle-tested engine. The module
  is confined to `codesearch/` so the root module's build graph is unchanged and
  an embedder that does not import `codesearch` pays nothing for zoekt. The
  `codesearch/policy_test.go` file replicates the root's cgo and cross-target
  gates for this module, plus a forbidden-import check that covers
  `codesearch`'s **direct** imports only. zoekt's own index and search packages
  pull in gRPC, Prometheus and sentry transitively, and that graph is accepted
  ([`errata/03_forbidden_imports_direct_only.md`](errata/03_forbidden_imports_direct_only.md)).
