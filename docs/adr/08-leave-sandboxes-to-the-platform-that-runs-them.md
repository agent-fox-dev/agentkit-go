# ADR 08: Leave sandboxes to the platform that runs them

- **Status:** Accepted
- **Date:** 2026-11-19

## Context

AgentKit had its own sandboxes (PRD 11, specs 13 to 19 and 25): the `sandbox`
module with the `agentkit-sandboxd` agent and its gRPC protocol, proxy tools,
an rsync-based sync and export, the local, Podman, Landlock and gateway
providers, the `sandbox/gatewayd` server with `agentkit-gateway`, and the
`containers/agentkit-sandbox` image. It was about 15 000 lines of production
Go and as many of tests. Inside the repository only three examples
(`examples/sandbox`, `examples/gateway` and `examples/harness`) imported it,
and nothing outside did.

Parley now owns sandboxes for the agent-fox projects: an OpenShell sandbox
born with a fork checked out, with Parley's credentials, network policy,
quotas and checkpoints. The OpenShell provider PRD 11 planned was never
built, and with Parley it has no purpose: the agent, or its tools, run inside
the sandbox Parley made.

PRD 10's rule decides it: is a part here because the model is bad at
judgment, or because AgentKit chose to own the whole stack? The tools are the
first kind. Sandboxes, once Parley exists, are the second. PRD 15 records the
decision; ADR 05 (the sandbox's module and sync) and ADR 06 (the gateway's
authentication) describe code that no longer exists.

## Decision

**The sandbox is deleted whole, in one cut.** The `sandbox` module with its
providers, agent, protocols and internal packages, `sandbox/gatewayd` and
`agentkit-gateway`, `containers/agentkit-sandbox`, `.dockerignore`, the three
examples, and the sandbox, gateway, container and `proto` make targets are
gone. No Landlock library is kept. `MODULES` is the root, `codesearch`,
`mcpserve` and `examples/indexedsearch`.

**No module requires rsync or go-landlock, and none requires gRPC or protobuf
directly.** gRPC and protobuf stay as indirect requirements of the root and
`mcpserve` (through the Vertex AI dependencies) and of `codesearch` (through
zoekt); no removal can change that.

**`v0.9.1` is the last release whose tree holds the sandbox.** It is tagged
on `f421f0b`, whose code is the code the removal started from (the commits
between them change only `AGENTS.md`, `.specs/` and `docs/prd/`). The
`sandbox/`, `codesearch/` and `mcpserve/` modules were not tagged at
`v0.9.1`: the owner waived those tags when the removal was implemented. A
consumer that still needs the sandbox module pins it by commit
(`go get github.com/agent-fox-dev/agentkit-go/sandbox@f421f0b`), which Go
resolves to a pseudo-version.

**`codesearch/go.sum` keeps the two `agentkit-go v0.9.0` lines** that
`make tidy` would drop because of the module's local `replace`: they were
added on purpose so the module resolves for consumers. `make tidy` is a
no-op for every other `go.mod` and `go.sum`.

**Nothing under `.github/workflows` changed** (the owner's decision).

## Consequences

- On a host without a sandbox the shell guard is the only boundary again, as
  before PRD 11. `guard.AllowAll` fits only a process that already runs
  inside an isolation boundary someone else owns.
- AgentKit supplies the hands that work inside someone else's sandbox; it
  creates, isolates, syncs and destroys none.
- `examples/harness` is gone until the `agentkit-tools` command (spec 32)
  and its example replace it.
- `.github/workflows/build-sandbox-containers.yaml` still starts on every
  push to `main` and builds `containers/agentkit-sandbox/Containerfile`,
  which is gone, so it fails until the owner changes or removes it.
- ADR 05 and ADR 06 are superseded by this one and kept as the record.
