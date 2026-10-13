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
