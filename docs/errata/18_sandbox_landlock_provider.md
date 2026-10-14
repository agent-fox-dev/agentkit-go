# Erratum: spec 18 `sandbox_landlock_provider`

These are the places the delivered code and its tests differ from the
spec's text, and why.

## Whole requirements in a task's criteria (D-1)

Tasks 2, 6, 7 and 8 list `18-REQ-2`, `18-REQ-6`, `18-REQ-8` and `18-REQ-9`
in `criteria`, which are requirement ids. They were read as every
criterion of that requirement.

## spec_tests runs from sandbox/ (D-2)

`tasks.json`'s `spec_tests` (`cd sandbox && go test -count=1 ./landlock/`)
uses a shell operator; it ran as `go test -count=1 ./landlock/` from
`sandbox/`.

## The example reuses runOn (D-3)

Task 10 asks for `runWith(ctx, w, dir, prov, spec)` in `examples/sandbox`.
Spec 17 had already split the example into `run` (on `local`, which
`main_test.go` calls) and `runOn(ctx, w, dir, p, spec)`, the same function.
`--landlock` and `--agent` use `runOn`.

## Linux behaviour verified in a container (D-4)

macOS has no Landlock. The Linux tests ran in a container on podman-machine
(`registry.redhat.io/ubi10/go-toolset`, Go 1.27.1, non-root, kernel
Landlock ABI 9, the agent's applied ABI capped at 4). golangci-lint is not
installed there, so `sandbox/landlock` was also linted from macOS with
`GOOS=linux`.

## The default Root fails check 4 where HOME is under a system directory (D-5)

In that container `HOME` is `/opt/app-root/src`, beneath `/opt`, a
read-only root of the agent's ruleset, so the default `Root`
(`<UserCacheDir>/agentkit-sandbox-landlock`) is refused after `Hello` by
18-REQ-5.8, as the spec intends. The tests use temporary `Root`s.

## go-landlock is in sandbox/landlock's graph, through gokrazy/rsync (D-6)

18-REQ-10.1 asks that `sandbox/landlock` import neither go-landlock nor
`sandbox/internal/confine`, "directly or transitively", and TS-18-62 that
`go list -deps ./landlock` list neither. The provider must attach with
`attach.Dial` (18-REQ-5.5), and `attach` uses `sandbox/internal/rsyncx`,
which imports gokrazy/rsync's `rsyncd`, whose `internal/restrict` imports
go-landlock. Every provider has that edge (`go list -deps ./local` shows
it too); `rsyncd` applies a ruleset only when an rsync module asks for
one (`rsyncd/restrictmodules.go`), which nothing on the host does, and
specs 14 and 15 test that the host stays unconfined. TS-18-62
(`internal/policy/landlock_provider_test.go`) therefore asserts that
`confine` is absent from the graph and that no package in it but
`github.com/gokrazy/rsync/internal/restrict` imports go-landlock; a
go-landlock import in `sandbox/landlock` fails it. TS-18-64 checks the
runtime half: the embedder stays unconfined.
