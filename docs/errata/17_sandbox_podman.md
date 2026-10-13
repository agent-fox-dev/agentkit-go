# Erratum: spec 17 `sandbox_podman`

These are the places the delivered code and its tests differ from the
spec's text, and why.

## The test commands are run one part at a time (D-1)

`tasks.json`'s `linter` (`make vet && make lint`) and `spec_tests`
(`(cd sandbox && go test …) && go test …`) use shell operators. Each part
ran as its own command, with the same effect: `make vet`, then `make lint`;
the sandbox module's packages from `sandbox/`, then `./internal/policy/...`
from the root.

## Whole requirements in a task's criteria (D-2)

Tasks 3, 4, 9 and 10 list `17-REQ-2`, `17-REQ-4`, `17-REQ-8` and `17-REQ-9`
in `criteria`, which are requirement ids, not criterion ids. They were read
as every criterion of that requirement.

## TS-17-60 compares the graph and the direct set separately (D-3)

TS-17-60 says to compare the sandbox module's direct requirements with
`internal/policy/testdata/sandbox-go-list-m-all.txt`. That file is the whole
`go list -m all` graph, not a direct set. The test compares the module's
graph with that snapshot, byte for byte, and its direct set with the set
TS-14-42 pins (`internal/policy/deps_17_test.go`).

## Verified with Podman 6 on podman-machine; Docker and krun were not run (D-4)

The integration and smoke tests ran with Podman 6.1.4 (crun) on
podman-machine, linux/arm64. Docker and the krun runtime were not
installed, so the Docker variant is checked only offline (the `Args` and
`InitArgs` goldens and the fake runner), and TS-17-58 skips.

## Podman adds HOSTNAME too

17-REQ-9.5 and TS-17-55 expect the container's environment to be `Spec.Env`
plus the five defaults, "and on Docker also HOSTNAME". Podman 6.1.4 sets
`HOSTNAME` as well, even with `--unsetenv-all`:
`podman run --rm --unsetenv-all --entrypoint /usr/bin/env <image>` prints
`HOSTNAME=<container id>`. It is the container's own hostname, not the
host's, and the provider cannot remove it without emitting a flag 17-REQ-4
does not allow. TS-17-55 (`sandbox/podman/integration_test.go`) accepts
`HOSTNAME` from either engine, requires it on Docker, and fails if it equals
the host's hostname.

## TS-17-55 leaves out the tool shell's own variables

`execute` runs its command through bash, which exports `PWD`, `SHLVL`, `_`
(and `OLDPWD` after a `cd`) to the commands it starts. They are the shell's,
not the container's, so TS-17-55 removes them from the `env` output before
comparing names.

## The host-side TTL counts from the expiry label's creation time

17-REQ-5.1 takes `created` before `volume create`, and the expiry label is
`created + TTL` (17-REQ-6.7), but attach's host timer starts only after
`Hello`. A slow `Create` (an image pull, a cold start) would let the sandbox
outlive its label, and another process's `Reap` could then remove a live
container. `Create` gives attach `TTL - (now - created)`
(`sandbox/podman/create.go`), so the destroy step runs at the label's
expiry; `Spec().TTL` still reports the effective TTL
(`sandbox/podman/sandbox.go`). `TestHostTTLFromCreated` in
`sandbox/podman/conformance_test.go` checks it.

## Engine output is parsed among stderr lines

`tools.RunArgv` interleaves stdout and stderr, and a docker shim prints
"Emulate Docker CLI using podman…", and rootless podman `WARN[…]` lines, to
stderr on every command. The `--version` parser takes the first line that
begins `podman version` or `Docker version` (17-REQ-2.3 says the output
"begins" with it); `Reap` skips listing lines that are not a single word and
reads the JSON object's line of the inspect output.
`TestEngineOutputWithStderrNoise` checks both.

## A GitRepo sync that outlives its sandbox removes the volume again

If the sandbox is closed, or reaches its TTL, while the init step runs, the
destroy step may remove the volume before the init container's `--mount`
re-creates it, unlabelled, where `Reap` cannot see it. After the init step
the provider checks `attach.Live`; when the sandbox is gone it removes the
volume again and returns the liveness error (`sandbox/podman/gitrepo.go`,
`TestGitRepoRacingClose`).

## Podman's --read-only adds more tmpfs mounts

Podman's `--read-only` defaults to `--read-only-tmpfs=true`, which also
mounts tmpfs at `/run`, `/var/tmp` and `/dev/shm`. 17-REQ-4 allows no other
flag, so they stay; the agent's Landlock ruleset keeps them read-only for the
tools, which TS-17-57 observes (`Permission denied` on `/var/tmp`). The docs
say so instead of calling `/tmp` the only writable directory.

## Known limitation: the memory-cgroup OOM killer can pick the agent

17-REQ-9.6 expects a command that exceeds `MemoryBytes` to be killed while
the sandbox stays live. TS-17-56 passed in 15 of 16 runs on Podman 6.1.4; in
the other, the kernel's memory-cgroup OOM killer chose `agentkit-sandboxd`
(about 4,100 resident pages) over the allocating `bash` (about 470 at that
moment), and the sandbox became unavailable. Every process in the container
has the same `oom_score_adj` (200). Making the killer prefer the tools'
processes needs the agent to raise their `oom_score_adj` (an unprivileged
process may raise it), which is an agent change this spec excludes; the
provider's flag list has no `--oom-score-adj`. TS-17-56 is left unweakened,
and the limitation is open for a later spec.
