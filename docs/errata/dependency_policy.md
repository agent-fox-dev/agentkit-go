# Erratum: stdlib-only is a preference, not a gate

**Relates to:** REQ-GO-11, REQ-GO-13 (`docs/prd/agent-kit-prd.md`); the
dependency rulings in `docs/DEPS.md`.
**Status:** accepted by the project owner in
[agent-fox-dev/agentkit-go#48](https://github.com/agent-fox-dev/agentkit-go/issues/48).

## What the PRD said

REQ-GO-11 held the root module to **nothing outside the Go standard library**,
and REQ-GO-13 made that a test: `internal/policy/deps_test.go` walked
`go list -deps ./...` and failed on any module absent from an `allowedModules`
map, where adding a dependency was an edit to that map. A second test,
`outline`'s TS-01-65, failed on any `require` line in the root `go.mod`.

## Why that is relaxed

The owner's ruling, on issue #48: the strict "no imports other than stdlib"
rule is no longer a hard requirement for the project, because dealing with the
codebase becomes too complicated over time under it. A hard allowlist turns
every dependency question into a policy dispute, including ones where a mature
module is plainly the better answer.

## What changed

- The stdlib-only gate is gone. `allowedModules` and `TestNoUnapprovedModules`
  are deleted from `internal/policy/deps_test.go`, and the "go.mod has no
  `require` line" assertion is dropped from `outline`'s TS-01-65 (now
  `TestInternalPolicyGreen_TS_01_65`).
- The policy is now: **the standard library is preferred; a third-party module
  may enter the root module when `docs/DEPS.md` says what it buys and why
  hand-rolling is not credible; cgo is never allowed.** `go.mod` is the
  authority on what the root requires. `docs/DEPS.md` is documentation of why,
  not an allowlist the build checks.
- REQ-GO-13's goal that the policy ships as a test now covers only what is not
  a matter of taste: cgo-freedom and the four-target cross build.
  `TestDependencyPolicyIsRecorded` keeps this erratum and
  `docs/DEPS.md` in step.

## What did not change

- **cgo is still rejected.** `TestNoCgoOutsideStdlib` (run with
  `CGO_ENABLED=1`, which is load-bearing) and `TestCgoProbeIsArmed` are
  untouched. Cgo-freedom, not module count, is what determines whether the SDK
  cross-compiles.
- **The cross-target gate is unchanged.** `TestCrossTargetBuildAndVet` builds
  and vets linux/amd64, linux/arm64, darwin/arm64 and windows/amd64 with
  `CGO_ENABLED=0` (NFR-COMPAT-06), in the root and in each nested module that
  carries its own copy.
- **Nested modules remain available.** They are still the only mechanism in Go
  that keeps a dependency out of the root's `go.mod`, `go.sum` and downstream
  SBOMs. `codesearch/`, `difftest/` and the examples keep theirs; a nested
  module is now a choice to be weighed, not the only door.
- **`outline`'s own design is unchanged.** `TestOutline_StdlibOnly_TS_01_64`
  pins that package's imports and build constraints. That is a decision about
  `outline`, not about the project.

## Existing code

Nothing is required to be rewritten. The hand-rolled TOML reader, the bounded
JSON parser and the provider clients (`docs/DEPS.md` R3–R5) were built for
reasons beyond module count — bounds before allocation, duplicate-key
rejection, tracking each vendor's wire format directly — and those reasons
stand. Comments elsewhere in the tree that cite REQ-GO-11 to explain why a piece
of code was hand-rolled describe history and are left as written.

Acceptance criteria in specs 01–03, and the goals in PRDs 04 and 05, that say
"`allowedModules` unchanged" or "`go.mod` gains no `require`" were true of those
deliveries and are likewise left as written. This erratum supersedes them as
standing policy.

The PRD text is left as written; REQ-GO-11 and REQ-GO-13 carry a pointer here.

A related, spec-specific decision about what `codesearch`'s forbidden-import
check covers is recorded in
[`03_forbidden_imports_direct_only.md`](03_forbidden_imports_direct_only.md).
