---
spec_id: "31"
spec_name: "sandbox_removal"
title: "Drop the sandbox module and leave sandboxes to the platform that runs them"
status: "active"
created_at: "2026-11-19T15:24:22Z"
intent_hash: "62cb5576a61b28c1a3a4bd15b8470508b4e22218ceb7b9678b642f800969e55e"
contract_hash:
  requirements.json: "000fe47108c8ac78c2fa83789102fb169f1294c6d1e28c66752ef5227f155fda"
  tasks.json: "45da913dc5893bee9c58038046bd31608f50608ece3d49fe225ff29ddf9693fa"
schema_version: 3
source: "docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md"
supersedes: ["13", "14", "15", "16", "17", "18", "19", "25"]
---
# Drop the sandbox module and leave sandboxes to the platform that runs them

## Intent

AgentKit stops creating and managing sandboxes itself. Isolation, lifetime,
credentials and file transfer belong to the platform that owns the sandbox
(Parley on OpenShell), and AgentKit supplies only the hands that work in it.
The repository drops its sandbox module, its gateway, its sandbox image and
their examples, and a pinned consumer can still reach the last release that
had them.

## Goals

1. The repository holds no `sandbox/` tree, no gateway, no sandbox image and
   no `examples/sandbox`, `examples/gateway` or `examples/harness`, and no
   sandbox, gateway, container or `proto` make target.
2. No `go.mod` of the repository requires `github.com/gokrazy/rsync` or
   `github.com/landlock-lsm/go-landlock` at all, or `google.golang.org/grpc`
   or `google.golang.org/protobuf` directly.
3. `make check` passes over the modules that remain: the root, `codesearch`,
   `mcpserve` and `examples/indexedsearch`.
4. The last commit that still has the sandbox is the release `v0.9.1`,
   reachable by its root, `codesearch/`, `sandbox/` and `mcpserve/` tags, and
   `docs/releasing.md` names it.
5. Every reference doc, README, generated doc and doc comment says what is
   true after the removal, and a new ADR records the decision, superseding
   ADR 05 and ADR 06.

## Non-goals

- The `agentkit-tools` command: the tool set and its flags, the MCP session
  mode, the single-call mode, the `tools` listing, the example that replaces
  `examples/harness`, and its `docs/cli.md` section. This is the second scope
  of PRD 15, its own package (`tool_server_command`).
- The index that outlives the process: `--index-dir`,
  `AGENTKIT_INDEX_DIR`, reuse across processes and `find_symbol` from the
  stored index. This is the third scope of PRD 15, its own package
  (`persistent_code_index`).
- Any change under `.github/workflows`, including
  `build-sandbox-containers.yaml` and the reusable `inc-build.yaml`
  (owner's decision, Design Decision 4).
- Creating, isolating, listing or destroying sandboxes, and moving files in
  or out of one. Parley does this (its PRD §13).
- Confining `execute` on a host without a sandbox. After this package the
  shell guard is again the only boundary there, as before PRD 11.
- Creating or pushing the `v0.9.1` tags. The owner does it before the
  removal is implemented (Design Decision 2).
- Moving spec 25 or any other package to `superseded`. That is a lifecycle
  move of those packages, made by their owner.
- Changes to `mcpserve`'s API, to the tool definitions, to code mode, or to
  `codesearch`.
- `hack/rmi-untagged-images.sh`, a generic helper that removes untagged
  Podman images; it is not part of the sandbox and stays.

## Background

AgentKit has its own sandboxes (PRD 11, specs 13 to 19 and 25): the
`sandbox` module (`sandbox/go.mod`) with the `agentkit-sandboxd` agent
(`sandbox/cmd/agentkit-sandboxd`) and its gRPC protocol
(`sandbox/internal/sandboxpb`), proxy tools, an rsync-based sync and export
(`sandbox/internal/rsyncx`), the Podman, Landlock, local and gateway
providers (`sandbox/podman`, `sandbox/landlock`, `sandbox/local`,
`sandbox/gateway`), the `sandbox/gatewayd` module with `agentkit-gateway`,
and the `containers/agentkit-sandbox` image. It is about 15 000 lines of
production Go and as many of tests. Inside the repository only
`examples/sandbox`, `examples/gateway` and `examples/harness` import it; the
project owner confirmed nothing outside does.

Parley now owns sandboxes for the agent-fox projects. A Parley sandbox is an
OpenShell sandbox born with a fork checked out, with Parley's credentials,
network policy, quotas and checkpoints. The planned OpenShell provider of
PRD 11 was never built, and with Parley it has no purpose. The decision rule
of PRD 10 applies: is this here because the model is bad at judgment, or
because we chose to own the whole stack? The tools are the first kind.
Sandboxes, once Parley exists, are the second.

What the removal touches outside the deleted trees, as the repository stands:

- The `Makefile`: `MODULES` lists `sandbox`, `sandbox/gatewayd`,
  `examples/sandbox`, `examples/gateway` and `examples/harness`; `doc` loops
  over `sandbox` and `sandbox/gatewayd`; `LDFLAGS` stamps
  `sandbox/internal/buildver`, and nothing reads `LDFLAGS`, `VERSION`,
  `COMMIT` or `BUILD_TIME` but it and the removed targets; the variables
  `CONTAINER_REGISTRY`,
  `SANDBOX_IMAGE`, `SANDBOX_IMAGE_TAG`, `SANDBOX_TEST_ENV`,
  `AGENTKIT_SANDBOX_GIT_URL`, `PROTO_DIR`, `PROTOC_GEN_GO` and
  `PROTOC_GEN_GO_GRPC`; the targets `proto`, `build-containers`,
  `build-sandbox-container`, `sandbox-podman-test`, `sandbox-landlock-test`,
  `sandbox-gateway` and `sandbox-gateway-test`.
- `internal/policy/builds_test.go` reads `MODULES` from the `Makefile` and
  builds and vets every module for four targets and the host
  (`TestCrossTargetBuildAndVet`, `TestHostCgoBuildAndVet`; slow, skipped
  under `-short`).
- Test helpers only the sandbox uses: `testutil.Tar` in
  `internal/testutil/testutil.go` (with `TestTarHoldsTheFilesInNameOrder`)
  and `Tree.Tar` in `internal/toolconformance/toolconformance.go`.
- `.dockerignore`, which exists for the sandbox image's build context, and
  the `/agentkit-sandboxd` line of `.gitignore`.
- Doc comments that describe sandbox behaviour: `workspace/workspace.go`,
  `guard/guard.go`, `mcpserve/doc.go`, `mcpserve/buildinfo.go`, the comment
  of `mcpserve/go.mod`, `internal/toolconformance/toolconformance.go` and
  `tools/for_test.go`. (`examples/codingagent/main.go` and `codemode` use
  "sandbox" in its general sense and stay as they are.)
- Docs: `README.md`, `examples/README.md`, `examples/codingagent/README.md`,
  `docs/DEPS.md`, `docs/cli.md`, `docs/api.md`, `docs/architecture.md`,
  `docs/guide.md`, `docs/configuration.md`, `docs/development.md`,
  `docs/releasing.md` and `docs/generated/` (`docs/generated/sandbox/`, and
  the comments that `make doc` copies into the generated docs of `guard`,
  `workspace`, `codemode` and `mcpserve`).
- ADR 05 (`docs/adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md`)
  and ADR 06 (`docs/adr/06-authenticate-every-gateway-rpc-before-reading-it.md`),
  both `Status: Accepted`.
- The release tags on origin are `v0.9.0` and `codesearch/v0.9.0`; `sandbox/`
  and `mcpserve/` have never been tagged, so no consumer can pin the sandbox
  module today.

## Requirements

### The last release with sandboxes

Before the removal, the owner runs `make check-release` on `main` and tags
the commit the removal starts from as the release `v0.9.1`: `v0.9.1`,
`codesearch/v0.9.1`, `sandbox/v0.9.1` and `mcpserve/v0.9.1`, all on that one
commit, pushed to origin. The implementer does not create them. They check
that all four exist and point at the commit the removal branch starts from,
and they stop before deleting anything if any is missing or points elsewhere,
naming each such tag.

After the removal, `docs/releasing.md` names `v0.9.1` as the last release
with sandboxes and says that `sandbox/v0.9.1` is the version to pin for them.
Its release steps tag the root, `codesearch/` and `mcpserve/` modules, and
it describes no image and no gateway.

### Removing the code and the modules

The repository holds none of: the `sandbox` module and its providers, agent,
protocols and internal packages; `sandbox/gatewayd` and `agentkit-gateway`;
`containers/agentkit-sandbox`; `examples/sandbox`, `examples/gateway` and
`examples/harness`; `.dockerignore`; the `/agentkit-sandboxd` line of
`.gitignore`; the test helpers only the removed code used.

- `MODULES` lists exactly `.`, `codesearch`, `mcpserve` and
  `examples/indexedsearch`, and every one of them builds and vets for every
  cross target and the host.
- No `go.mod` requires `github.com/gokrazy/rsync` or
  `github.com/landlock-lsm/go-landlock`, and `go list -m all` in no module
  lists them. No `go.mod` requires `google.golang.org/grpc` or
  `google.golang.org/protobuf` without `// indirect`: they stay in the graphs
  of the root and `mcpserve` (through the Vertex AI dependencies) and of
  `codesearch` (through zoekt).
- `make tidy` leaves every `go.mod` and `go.sum` as committed.

### Build tooling

- The `Makefile` defines none of the targets and variables listed in the
  background; `make help` lists no target that names a sandbox, the gateway,
  a container or `proto`, and `make proto` fails as an unknown target.
- `make doc` documents the root, `codesearch` and `mcpserve` modules only;
  `docs/generated/` holds no `sandbox/` directory, and a second run leaves
  it unchanged.
- No file under `.github/workflows` changes.
- `make check` exits 0.

### Docs and doc comments

- The reference docs and READMEs listed in the background describe no
  sandbox module, provider, agent, gateway, sandbox image or `proto` target
  as part of AgentKit: none of the names `agentkit-sandboxd`,
  `agentkit-gateway`, `gatewayd`, `sandbox/` as a path of this repository,
  `sandbox.` as a Go identifier, `podman` or `landlock` appears in them,
  except in `docs/releasing.md`'s statement of the last release with
  sandboxes. Each one's **Contents** index matches its headings. The word
  "sandbox" in its general sense ("the guard is a floor, not a sandbox",
  "a sandbox someone else runs") stays.
- `docs/DEPS.md` drops the rulings and allowlist rows of
  `google.golang.org/grpc`, `google.golang.org/protobuf`,
  `github.com/gokrazy/rsync` and `github.com/landlock-lsm/go-landlock`, and
  no longer says the list covers the `sandbox` or `sandbox/gatewayd` module.
- `docs/cli.md` stays and says in one line that the repository ships no
  command-line program; the `agentkit-tools` package adds its section.
- The doc comments listed in the background say what is true afterwards and
  name no identifier, package or command of the removed module; the guard's
  comments say the guard is the only boundary on a host without a sandbox,
  and that `guard.AllowAll` fits a process already inside an isolation
  boundary someone else owns. `docs/generated/` is regenerated from them.
- Archived records (`docs/prd/`, `docs/archive/`, `.specs/`) are not
  rewritten.

### The decision record

- A new ADR, `docs/adr/08-leave-sandboxes-to-the-platform-that-runs-them.md`,
  `Status: Accepted`, records the decision: the sandbox module is deleted
  whole, why (Parley owns sandboxes; the PRD 10 rule), and the consequences
  (the shell guard is the only boundary on a bare host, `v0.9.1` is the last
  release with sandboxes, `examples/harness` is gone until `agentkit-tools`
  replaces it, the workflow left under `.github/workflows`).
- ADR 05 and ADR 06 keep their bodies; their status line reads that each is
  superseded by ADR 08, with a link. Neither is deleted.

### End-to-end flows

1. **A maintainer checks the tree after the removal.** The maintainer runs
   `make check` at the root: fmt, vet, lint and the short tests run in the
   four remaining modules and pass. `make doc` and `make tidy` change
   nothing. `make help` lists no removed target. No removed tree is tracked
   and no `go.mod` names rsync or go-landlock.
2. **A pinned consumer finds the last release with sandboxes.** A consumer
   reads `docs/releasing.md`, finds `v0.9.1`, and resolves `sandbox/v0.9.1`
   with git: the tagged tree holds `sandbox/go.mod`, and the tag sits on the
   commit the removal follows.

## Design Decisions

1. **Delete the sandbox module whole, in one cut** (owner's decision, PRD 15
   D1). Nothing outside the repository uses it and confined local runs are
   not needed. No Landlock library is kept.
2. **The last build with sandboxes is the ordinary release `v0.9.1`, and the
   owner tags it** (owner's decision, this run). Releasing needs
   `make check-release`, which agents never run, so the owner runs it on
   `main` and pushes `v0.9.1`, `codesearch/v0.9.1`, `sandbox/v0.9.1` and
   `mcpserve/v0.9.1` before the removal is implemented. A patch version,
   because nothing since `v0.9.0` changed the API. Go resolves nested modules
   only from version tags, so a pinned consumer needs `sandbox/v0.9.1`.
3. **The removal lands first of PRD 15's three scopes** (owner's decision,
   this run). `examples/harness` is deleted before its replacement exists;
   the replacement example and the `docs/cli.md` section of `agentkit-tools`
   land with the `tool_server_command` package.
4. **Nothing under `.github/workflows` changes** (owner's decision, this run),
   overriding PRD 15's removal of the image's publishing workflow.
   `build-sandbox-containers.yaml` starts on every push to `main` and builds
   `containers/agentkit-sandbox/Containerfile`, which this package deletes, so
   that workflow fails until the owner changes it.
5. **"No gRPC or protobuf requirement" means no direct one** (decided from
   the code). `google.golang.org/grpc` and `google.golang.org/protobuf` are
   indirect requirements of the root and `mcpserve` (Vertex AI) and of
   `codesearch` (zoekt), which no removal can change. rsync and go-landlock
   leave every module graph.
6. **Test helpers that only the removed code used go with it** (decided from
   the code): `testutil.Tar` with its test, and `toolconformance`'s
   `Tree.Tar`. `toolconformance` stays: `tools` runs its case table.
7. **`.dockerignore` and the `/agentkit-sandboxd` ignore line go** (decided
   from the code): both exist only for the deleted image and binary.
8. **`docs/cli.md` stays with one line** (steering): a repository without a
   CLI keeps the file and says it has none, until `agentkit-tools` arrives.
9. **Profile: full.** The removal is verified end to end by the maintainer's
   `make check` and the consumer's tag lookup, which the integration task
   runs.
10. **The package lists specs 13 to 19 and 25 in `supersedes`**, whose code
    it removes. Changing their status is a separate lifecycle move by their
    owner.

## Dependencies

- PRD 15 (`docs/prd/15-serve-the-tools-from-one-command-and-drop-the-sandbox.md`):
  the source; this package is its first scope.
- PRD 11 (`docs/prd/11-run-tools-in-an-isolated-sandbox.md`) and PRD 13
  (`docs/prd/13-authenticate-gateway-tokens-with-a-callback.md`): superseded.
- Spec 25 `gateway_authenticator` (active, all tasks done): its code is
  removed. Archived specs 13 to 19: their code is removed.
- ADR 05 and ADR 06: superseded by ADR 08, which this package writes.
- Parley (`github.com/agent-fox-dev/parley`, PRD §13): the platform that owns
  sandboxes from now on.

## Glossary

- **The removal**: the change this package makes, on the branch that starts
  from the commit tagged `v0.9.1`.
- **The last release with sandboxes**: `v0.9.1`, with its `codesearch/`,
  `sandbox/` and `mcpserve/` tags on the same commit.
