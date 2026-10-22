---
spec_id: "16"
spec_name: "sandbox_images"
title: "Sandbox images: the base and overlay images carrying agentkit-sandboxd, local builds and the release workflow"
status: "active"
created_at: "2026-10-12T15:01:42.819368Z"
updated_at: "2026-10-12T15:01:42.819368Z"
intent_hash: "1bc028b13b4ca442d8801d54651481d6ebb5bfd6c1d76bfeb5301d29a766d93b"
schema_version: 2
source: "docs/prd/11-run-tools-in-an-isolated-sandbox.md"
---
## Intent

Give every container-based sandbox provider one trustworthy, versioned source for the sandbox agent. That source is two OCI images that carry `agentkit-sandboxd` at `sandbox.AgentPath`. The base image is a small Debian image that runs as is. The overlay image holds a single static file, which an embedder copies into its own toolchain image. Both images are built from this repository by one Containerfile. The build is the same on a developer's machine and in the release workflow. Each image is stamped with the module version it was built from. Each release is published to GHCR only after the images pass an end-to-end check over the agent protocol.

## Goals

1. **Local build.** On a Linux or macOS host with `podman` or `docker`, `make sandbox-images` builds `quay.io/agentfox/agentkit-sandbox:latest` and `quay.io/agentfox/agentkit-sandboxd:latest` for the host platform. `make sandbox-images-test` then passes against them.
2. **Base image check.** An agent started from the base image over `run -i` and reached with `attach.Dial` reports:
   - `OutlineBackend == "tree-sitter"`, `Git == true` and `Workdir == "/workspace"`;
   - the expected version, which is `(devel)` when no version was stamped.

   Its `Tools` call passes the host's definition-digest check. Through proxy `execute`, the process runs as uid and gid 1000, and `git`, `bash` and the CA bundle are present. Neither `rsync` nor `curl` is present. The agent binary is owned by root with mode 0755. A proxy `write_file` into the workspace succeeds. When the agent reports a Landlock ABI of 2 or more, writing to `/var/tmp` fails while writing to `$HOME`, `/tmp` and `/workspace` succeeds.
3. **Overlay image check.** The overlay image has exactly one layer, and `--version` runs inside it, which proves the binary is static. An image built as `FROM <base image>` plus the single line `sandbox.OverlayCopyLine(<overlay ref>)` runs the copied agent. That agent reports `OutlineBackend == "none"` and the expected version, and its `Tools` call passes the digest check.
4. **Release.** Pushing the git tag `vX.Y.Z` publishes both repositories:
   - with tag `X.Y.Z`, plus `X.Y` when the version has no prerelease part;
   - for `linux/amd64` and `linux/arm64`;
   - only after the image tests passed on the runner.

   An anonymous pull of each published tag works on each platform, and the agent's `--version` prints `agentkit-sandboxd vX.Y.Z (protocol M.N)`.
5. **Default image reference.** In a binary that depends on agentkit-go `vX.Y.Z`, `sandbox.DefaultImage()` returns `quay.io/agentfox/agentkit-sandbox:X.Y.Z` and `sandbox.OverlayImage()` returns `quay.io/agentfox/agentkit-sandboxd:X.Y.Z`. For a pseudo-version, `(devel)`, or a version with build metadata, both return `ErrNoPublishedImage`.
6. **`make check` stays offline.** It builds no image, needs no container engine and touches no network. The cross-target gate, the host-cgo gate, TS-09-20 and TS-12-14 pass. The root module gains no Go dependency. The exported surfaces of `agentkit`, `core`, `tools` and `codemode` do not change.

## Non-goals

- **The providers that run these images.** Each later scope of this split owns its part:
  - `sandbox_podman`:
    - passing `--entrypoint sandbox.AgentPath` and `sandbox.AgentArgs`;
    - `--init` for zombie reaping;
    - `--read-only`, tmpfs mounts, the workspace volume, `--userns` and seccomp;
    - the init-step `git clone`;
    - applying `DefaultImage()` when `Spec.Image` is empty;
    - the missing-agent error that prints `OverlayCopyLine`.
  - `sandbox_landlock_provider`: getting the agent binary onto a host. It uses `go install`, not an image.
  - `sandbox_gateway`: images for, or deployment of, `agentkit-gateway`.
- **Second-release work.** Images for the Kubernetes backend or for OpenShell.
- **Other release artifacts.** No GitHub release binaries, archives or Homebrew formulae, and no tagging of the Go module itself. Cutting the `vX.Y.Z` tag is a maintainer action.
- **General CI.** This spec adds no `make check` workflow on pull requests or pushes. The one workflow here builds, tests and publishes images.
- **Supply-chain extras.** No image signing (cosign), no SBOM, and no provenance policy beyond what `docker buildx` does by default.
- **More platforms or builds.** No platforms other than `linux/amd64` and `linux/arm64`. No multi-arch local builds through `make`. No reproducible, bit-identical builds, and no image size target.
- **More tools in the base image.** No language toolchains, `curl`, `rsync`, `tini`, `openssh-client` or editors. An embedder that needs a toolchain uses the overlay image.
- **Changing what the agent does.** No change to the agent's behaviour, flags, protocol or Landlock ruleset (13, 14, 15), except the build-time version stamp in Requirement 4.

## Background

**What exists in the repository today:**
- No container build files exist (no `Containerfile`, `Dockerfile` or `.dockerignore`).
- No `.github/` directory and no CI workflow exist.
- No git tags exist (`.git/refs/tags` is empty). The remote is `github.com/agent-fox-dev/agentkit-go`.
- The `Makefile` has `help`, `test`, `coverage`, `fmt`, `vet`, `lint`, `doc`, `tidy`, `check` and `clean-branches`. `help` lists every target that has a `## ` comment.
- `go.mod` declares `module github.com/agent-fox-dev/agentkit-go` and `go 1.27`.
- `LICENSE` is Apache-2.0.
- `/build/` is git-ignored.

**What the code requires of a build:**
- `outline/treesitter.go` is `//go:build cgo` and links the tree-sitter grammars. `outline/treesitter_nocgo.go` is the fallback, so without cgo, only Go has an outline backend. `docs/DEPS.md` calls tree-sitter "the only cgo code in the module".
- `internal/policy/crosstarget_test.go` builds and vets four targets with cgo off, and builds the host with cgo on. Its package holds only test files, which `go build ./...` accepts.
- `tools.ResolveShell` (`tools/proc_unix.go`) tries `/bin/bash`, then `bash`, then `sh`. A base image with bash at `/bin/bash` runs `execute` exactly as the tools package expects.
- `find_references` uses a workspace-local importer (`tools/ref_gotypes.go`) and needs no Go toolchain in the image.

**What the earlier scopes of this split deliver.** They are written, but not yet in the tree.
- **`13_sandbox_core`:**
  - `sandbox.AgentPath = "/usr/local/libexec/agentkit-sandboxd"` and `sandbox/cmd/agentkit-sandboxd`, which serves the agent service on stdin and stdout;
  - `--version`, which prints `agentkit-sandboxd <buildver.Version()> (protocol M.N)`;
  - `sandbox/internal/buildver`, whose `Version()` reads the agentkit-go module version from `runtime/debug` build info and falls back to `(devel)`;
  - `sandbox/internal/stdioconn` and `sandbox/internal/attach` (`Dial(ctx, net.Conn, Config)`). `Hello` reports the version, the workspace, the outline backend (`tree-sitter` in a cgo build, `none` otherwise) and whether `git` is present;
  - the `Tools` RPC, which refuses a sandbox whose tool-definition digests differ from the host's (`ErrToolMismatch`).

  13 deferred the images to this scope.
- **`14_sandbox_rsync_sync`:** the agent carries the rsync protocol itself (`gokrazy/rsync`), so no image needs an `rsync` binary.
- **`15_sandbox_landlock_agent`:**
  - The agent applies a Landlock ruleset first thing in `main`. It grants read and write beneath the workspace, `HOME` and the temporary directory, and read and execute beneath `/usr`, `/bin`, `/sbin`, `/lib*`, `/etc` and `/opt`.
  - It reports the result in `AgentInfo.Landlock`, and adds the `--network` flag and `sandbox.AgentArgs(spec)`.
  - The base image's cgo build links psx's cgo variant (through go-landlock). 15 left that build to this scope.
  - 15's gates assert that no `CGO_LDFLAGS_ALLOW` is introduced.

**PRD 11 (D5)** decided that the image carries the agent and that nothing is injected. It names the two image repositories, the Debian base with `git`, `bash`, `ca-certificates` and a non-root user, the `FROM scratch` overlay with a static agent, and the single `COPY --from` line an embedder adds. Step 2 of PRD 11's order of work names "the two Containerfiles, the release workflow that publishes them". This spec builds both images from a single Containerfile instead (DD 1).

## Requirements

### 1. One Containerfile with two targets

`sandbox/images/Containerfile` builds both images, with the repository root as the build context.

**Targets.** `--target base` builds the base image and `--target overlay` builds the overlay image. The overlay target depends only on the static build stage.

**Build stages.**
- The build stages run on the build platform: `FROM --platform=$BUILDPLATFORM golang:1.27-trixie`.
- They set `GOTOOLCHAIN=local`, so a `go` directive newer than the image's toolchain fails the build rather than downloading a toolchain.
- `go.mod` and `go.sum` are copied and `go mod download` runs before the rest of the source is copied.

**The cgo build** compiles `./sandbox/cmd/agentkit-sandboxd` with `CGO_ENABLED=1`, `GOOS=linux` and `GOARCH=$TARGETARCH`. When `TARGETARCH` differs from `BUILDARCH`, the stage installs Debian's `crossbuild-essential-$TARGETARCH` and sets `CC`:
- `x86_64-linux-gnu-gcc` for `amd64`;
- `aarch64-linux-gnu-gcc` for `arm64`.

**The static build** compiles the same package with `CGO_ENABLED=0`.

**Flags common to both builds:** `-trimpath`, `-buildvcs=false` and `-ldflags "-s -w -X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=${AGENTKIT_VERSION}"`.

**Constraints.**
- No `CGO_LDFLAGS_ALLOW` or other new environment variable is set.
- If the cgo link of psx fails without one, record the case in an erratum under `docs/errata/` named for this spec, and stop. This is the same discipline 15 applies.
- A `TARGETARCH` other than `amd64` or `arm64` fails the build with a message naming the two supported platforms.

**Build arguments.** `AGENTKIT_VERSION` and `AGENTKIT_REVISION` both default to empty. They feed the stamp and the OCI labels.

**`.dockerignore`.** A root `.dockerignore`, which both podman and docker read, keeps out of the context:
- `.git`, `.github`, `.specs`, `.claude` and `build`;
- `codesearch`, a nested module the agent does not import;
- `docs`, `coverage.*`, `*.test` and `.DS_Store`.

It must not exclude `go.mod`, `go.sum` or any root-module package.

### 2. The base image

The `base` target is `FROM debian:trixie-slim` and contains:

**Packages.** `git`, `bash` and `ca-certificates`, installed with `--no-install-recommends`, with the apt lists removed afterwards. Nothing else is added: no `rsync`, `curl` or `tini`.

**The user.** User `agentkit` with uid 1000, group `agentkit` with gid 1000, home `/home/agentkit` (owned 1000:1000) and login shell `/bin/bash`.

**Directories and files.**
- `/workspace` exists, is owned 1000:1000 and has mode 0755. It is not declared as a `VOLUME`.
- The system git config sets `safe.directory = *`.
- The cgo-built agent sits at `sandbox.AgentPath`, owned 0:0 with mode 0755.

**Image config.**
- `USER 1000:1000`, in numeric form.
- `WORKDIR /workspace`.
- `ENTRYPOINT ["/usr/local/libexec/agentkit-sandboxd"]`.
- No `ENV` beyond what `debian:trixie-slim` already sets.
- Labels:
  - `org.opencontainers.image.source=https://github.com/agent-fox-dev/agentkit-go`;
  - `org.opencontainers.image.licenses=Apache-2.0`;
  - `org.opencontainers.image.title`;
  - `org.opencontainers.image.description`;
  - `org.opencontainers.image.version`, from `AGENTKIT_VERSION`;
  - `org.opencontainers.image.revision`, from `AGENTKIT_REVISION`.

The documentation says that a provider must not rely on `ENTRYPOINT`. It passes `--entrypoint sandbox.AgentPath` itself, because an embedder's image built with the overlay has its own entrypoint.

### 3. The overlay image

The `overlay` target is `FROM scratch`, with a single `COPY` of the static agent to `sandbox.AgentPath`.
- The result is one layer, holding the agent owned 0:0 with mode 0755.
- It sets `ENTRYPOINT ["/usr/local/libexec/agentkit-sandboxd"]` and the same OCI labels as the base image.
- An embedder adds the agent to its own image with exactly the line that `sandbox.OverlayCopyLine` returns:

  `COPY --from=<overlay ref> /usr/local/libexec/agentkit-sandboxd /usr/local/libexec/`

### 4. The build-time version stamp

`sandbox/internal/buildver` gains an unexported package variable, `stamp string`, set only through `-ldflags -X`. `Version()` returns `stamp` when it is non-empty, and otherwise behaves exactly as 13 specified.

As a result:
- an agent built with `AGENTKIT_VERSION=v0.6.0` reports `v0.6.0` in `Hello` and in `--version`;
- an unstamped image build reports `(devel)`, because the context has no `.git` and the build uses `-buildvcs=false`;
- a host binary, which never sets the stamp, is unaffected.

Two tests cover it:
- An in-package test sets and clears `stamp`.
- A command test builds `agentkit-sandboxd` with `-X …buildver.stamp=v9.8.7` into a temp dir and runs `--version`. The output must be `agentkit-sandboxd v9.8.7 (protocol <ProtocolMajor>.<ProtocolMinor>)`, formatted from the constants. The test is skipped under `-short`.

### 5. Image references in package `sandbox`

`sandbox/image.go` adds:

```go
const (
    BaseImageRepo    = "quay.io/agentfox/agentkit-sandbox"
    OverlayImageRepo = "quay.io/agentfox/agentkit-sandboxd"
)
var ErrNoPublishedImage = errors.New("sandbox: no published image for this agentkit-go version")
func ImageTag(version string) (tag string, ok bool)
func DefaultImage() (string, error)   // BaseImageRepo + ":" + tag for buildver.Version()
func OverlayImage() (string, error)   // OverlayImageRepo + ":" + tag for buildver.Version()
func OverlayCopyLine(ref string) string
```

**`ImageTag`** accepts only a canonical release version: `v` followed by `MAJOR.MINOR.PATCH` with no leading zeros, and an optional `-prerelease`. It returns the version without its leading `v`. It returns `ok == false` for:
- the empty string and `(devel)`;
- any version carrying `+` build metadata, including `+dirty` and `+incompatible`;
- a Go pseudo-version, meaning a prerelease that ends, at its start or after a `.`, in a 14-digit timestamp, a `-` and 12 lowercase hex digits;
- anything else that is not canonical.

**`DefaultImage` and `OverlayImage`** return an error wrapping `ErrNoPublishedImage` when `ImageTag` refuses the build's version. The message names the version and says to set `Spec.Image`, or to build an image with `make sandbox-images`. Both are thin wrappers over an unexported function that takes the version as a parameter, which the tests drive.

**`OverlayCopyLine(ref)`** returns `"COPY --from=" + ref + " " + AgentPath + " " + path.Dir(AgentPath) + "/"`.

**Tests.** A table test covers `ImageTag`: release versions, prereleases, all three pseudo-version forms, `+dirty`, `+incompatible`, leading zeros, a missing `v`, `(devel)` and the empty string. It also covers the unexported reference builder and `OverlayCopyLine`.

### 6. Make targets

The `Makefile` gains two targets, each with a `## ` help line, and these variables:

| Variable | Default |
|---|---|
| `CONTAINER` | `podman` when on `PATH`, else `docker` |
| `SANDBOX_BASE_IMAGE` | `quay.io/agentfox/agentkit-sandbox` |
| `SANDBOX_OVERLAY_IMAGE` | `quay.io/agentfox/agentkit-sandboxd` |
| `SANDBOX_IMAGE_TAG` | `dev` |
| `SANDBOX_PLATFORM` | empty, meaning the host platform; when set, passed as `--platform` |
| `AGENTKIT_VERSION` | empty |
| `AGENTKIT_REVISION` | `git rev-parse HEAD`, or empty outside a checkout |

**`make sandbox-images`** runs `$(CONTAINER) build -f sandbox/images/Containerfile --target base` and then `--target overlay` from the repository root. It passes both build arguments and tags `$(SANDBOX_BASE_IMAGE):$(SANDBOX_IMAGE_TAG)` and `$(SANDBOX_OVERLAY_IMAGE):$(SANDBOX_IMAGE_TAG)`. When neither engine is found, it fails before building, with a message naming `podman`, `docker` and the `CONTAINER` variable.

**`make sandbox-images-test`** depends on `sandbox-images`. It runs `go test -count=1 -v ./sandbox/images/` with the environment of Requirement 7 set from these variables.

Neither target is part of `make check`.

### 7. End-to-end image tests (opt-in)

`sandbox/images/` is a test-only Go package. Its container tests read these variables:
- `AGENTKIT_SANDBOX_BASE_IMAGE` and `AGENTKIT_SANDBOX_OVERLAY_IMAGE`, the image references under test;
- `AGENTKIT_SANDBOX_IMAGE_VERSION`, the expected agent version. Empty means `(devel)`;
- `AGENTKIT_CONTAINER`. When it is unset, the tests use `podman`, then `docker`, from `PATH`.

**When a test skips.** It skips, and does not fail, under `-short`, when no engine is found, or when its image variable is unset. The skip message names `make sandbox-images-test`.

**The harness** starts an agent with:

`$CONTAINER run --rm -i --name agentkit-imgtest-<random hex> --network none --env HOME=/home/agentkit --env TMPDIR=/tmp --entrypoint <sandbox.AgentPath> <image> <sandbox.AgentArgs(spec)…>`

- `spec` is `{Network: NetworkNone, TTL: 10m, Workdir: "/workspace"}`.
- The harness wraps the child's pipes with `stdioconn.New` and attaches with `attach.Dial`.
- `Destroy` kills the child and runs `$CONTAINER rm -f <name>`.
- Cleanup always closes the sandbox and removes the container and any image the test built.

**The base image test** asserts:
- `Agent()`: `Version` equals the expected version; `ProtocolMajor == sandbox.ProtocolMajor`; `Workdir == "/workspace"`; `OutlineBackend == "tree-sitter"`; `Git == true`.
- `Tools(ctx, ToolOptions{})` succeeds.
- Proxy `execute`:
  - `id -u` and `id -g` print `1000`;
  - `git --version` exits 0;
  - `git config --system --get-all safe.directory` prints `*`;
  - `test -s /etc/ssl/certs/ca-certificates.crt` exits 0;
  - `command -v rsync` and `command -v curl` exit non-zero;
  - `stat -c '%u %a'` of the agent prints `0 755`;
  - `stat -c %u /workspace /home/agentkit` prints `1000` twice.
- Proxy `write_file` of `probe.txt` succeeds.
- When `Agent().Landlock.ABI >= 2`:
  - `touch /var/tmp/agentkit-probe` exits non-zero;
  - `touch "$HOME/p" /tmp/p /workspace/p` exits 0.

  Otherwise the test logs the Landlock note.
- `run --rm --entrypoint <AgentPath> <image> --version` prints `agentkit-sandboxd <expected> (protocol M.N)`.
- `image inspect`:
  - `User` is `1000:1000` and `Entrypoint` is `[AgentPath]`;
  - there are no `Volumes`;
  - the `Env` names are a subset of `{PATH}`;
  - the `source` and `licenses` labels are the values from Requirement 2.

**The overlay image test** asserts:
- `image inspect` shows exactly one entry in `RootFS.Layers`, and `Entrypoint` is `[AgentPath]`.
- `--version` prints the expected line.
- **A derived image**, built only when the base image variable is also set:
  - It is built from a temporary context whose Containerfile is `FROM <base>` followed by `OverlayCopyLine(<overlay>)`.
  - It starts through the harness.
  - It reports `OutlineBackend == "none"` and the expected version.
  - `Tools` succeeds, and proxy `execute` of `echo ok` succeeds.
  - It passes the same Landlock check as the base image.

### 8. Offline checks in `make check`

Requirements 4 and 5 run in `make check`. So does `sandbox/images/consistency_test.go`, which reads files and needs no engine. It asserts:
- The Containerfile has stages named `base` and `overlay`.
- The `overlay` stage starts `FROM scratch`.
- Both stages name `sandbox.AgentPath` as the agent's `COPY` destination and in `ENTRYPOINT`.
- The Containerfile carries the `-X` path `<module>/sandbox/internal/buildver.stamp=`.
- The Makefile's default `SANDBOX_BASE_IMAGE` and `SANDBOX_OVERLAY_IMAGE` equal `BaseImageRepo` and `OverlayImageRepo`.
- The release workflow names both repositories and `sandbox/images/Containerfile`.

The cross-target and host-cgo gates pass with the new test-only package. No file adds a Go module requirement.

### 9. The release workflow

`.github/workflows/sandbox-images.yml` has these settings:
- **Triggers:** `push` of tags matching `v*`, and `workflow_dispatch`.
- **Permissions:** top-level `permissions: contents: read`. Only the publishing job adds `packages: write`.
- **Concurrency:** grouped per ref, without cancelling a run in progress.

**Job `images`** (runs on `ubuntu-24.04`):
1. Check out the repository, and set up Go from `go-version-file: go.mod`.
2. **Version.** On a tag push, `GITHUB_REF_NAME` must match `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`, or the job fails.
   - The version is the ref name.
   - The image tags are the version without `v`, plus `MAJOR.MINOR` when there is no prerelease part.
   - On a dispatch, the version is empty and nothing is tagged for publishing.
3. **Test.** Run `make sandbox-images-test CONTAINER=docker SANDBOX_IMAGE_TAG=ci AGENTKIT_VERSION=<version> AGENTKIT_REVISION=$GITHUB_SHA`. This runs with the runner's default docker builder, before any buildx builder is set up, so the derived-image test resolves the locally built images. A test failure ends the job.
4. **Publish** (tag push only):
   - Set up QEMU and buildx.
   - Log in to `ghcr.io` with `GITHUB_TOKEN`.
   - Run `docker buildx build --platform linux/amd64,linux/arm64 -f sandbox/images/Containerfile --push` once for `--target base` and once for `--target overlay`, with the same build arguments and every computed tag on its repository.

**Job `verify`** (tag push only, needs `images`):
1. Do not log in. Set up QEMU.
2. For each repository, tag and platform, `docker pull --platform <p>` the reference.
3. Run `--entrypoint <AgentPath> … --version`. The output must start with `agentkit-sandboxd <version> `.

`verify` fails while a package is still private. The documentation explains the one-time visibility step and that `verify` can be re-run alone.

**Actions used.** Only `actions/checkout`, `actions/setup-go`, `docker/setup-qemu-action`, `docker/setup-buildx-action` and `docker/login-action`, each pinned to a major version tag. A dispatch run never logs in and never pushes.

### 10. Documentation

Per steering, run `make doc`, then update:
- **`README.md`:**
  - in the Sandboxes section, the two images and what each holds, with the `COPY --from` line;
  - `sandbox-images` and `sandbox-images-test` in the make targets table;
  - a link to `docs/releasing.md`.
- **`docs/architecture.md`:** a "Sandbox images" part of the Sandboxes section, covering:
  - one Containerfile with two targets, and the cross-compiling build stages;
  - the version stamp;
  - the root-owned agent and the uid-1000 user;
  - how `/workspace`, `/home/agentkit` and the system directories line up with 15's Landlock roots;
  - why there is no `ENV` and no `VOLUME`;
  - that providers pass `--entrypoint` and `--init`.
- **`docs/configuration.md`:**
  - `BaseImageRepo`, `OverlayImageRepo`, `ImageTag`, `DefaultImage`, `OverlayImage`, `OverlayCopyLine` and `ErrNoPublishedImage`;
  - a table of the base image's contents;
  - the tag scheme, including that a re-run rebuilds a tag, so an embedder who needs immutability pins a digest;
  - the make variables.
- **`docs/cli.md`:** `--version` reports the stamped release version in published images, and `(devel)` in unstamped builds.
- **`docs/releasing.md`** (new):
  - pushing `vX.Y.Z` publishes the images;
  - which tags are published;
  - making both GHCR packages public on the first release, then re-running `verify`;
  - re-runs;
  - the dispatch dry run;
  - local builds.

## Design Decisions

1. **One Containerfile with `base` and `overlay` targets, at `sandbox/images/Containerfile`, instead of PRD 11's "two Containerfiles".** Both images share the module download and the build stage, and keeping the file under `sandbox/` follows 13's rule that sandbox material lives in that tree.
2. **The runtime base is `debian:trixie-slim` and the builder is `golang:1.27-trixie`, referenced by tag rather than digest, with `GOTOOLCHAIN=local`.** Trixie is Debian stable at this writing. Tags let each release pick up security updates. `GOTOOLCHAIN=local` turns drift from `go.mod`'s `go 1.27` into a build failure instead of a silent download.
3. **The build stages cross-compile on the build platform with Debian's `crossbuild-essential-*`, for `linux/amd64` and `linux/arm64` only.** The Go and C compiles of tree-sitter then run natively and not under QEMU. arm64 covers Apple Silicon `podman machine` VMs, and amd64 covers CI and cloud hosts.
4. **The version is stamped with `-ldflags -X` into a new `buildver.stamp`, not derived from VCS.** VCS stamping would need `.git` in the build context and tag-aware checkouts in CI, and would silently produce a pseudo-version when either is missing. A build argument is explicit, and the stamp is the same locally and in CI. This is a small change to 13's package.
5. **The builds use `-trimpath` and `-buildvcs=false`.** No path from a runner or a developer's machine is embedded in the binary, and the unstamped version is a predictable `(devel)`.
6. **The base image's user is `agentkit`, uid and gid 1000, declared as numeric `USER 1000:1000`.** Kubernetes `runAsNonRoot` can verify only a numeric user. A fixed uid gives the workspace volume a known owner when the provider's runtime copies the image's `/workspace` into a fresh volume.
7. **No `ENV` and no `VOLUME` in either image.** The agent hands its own environment to every tool verbatim (13). Docker has no way to unset an image's `ENV`, so any variable the image declared would reach the model's commands. A `VOLUME` would create anonymous volumes that no provider tracks or reaps.
8. **Both images set `ENTRYPOINT` to the agent, but providers must not rely on it.** It makes `run … --version` work for humans and tests. Images that embedders build with `COPY --from` keep their own entrypoint, so providers always pass `--entrypoint`.
9. **The system git config sets `safe.directory = *`.** Under rootless Podman with `--userns=keep-id`, the process uid and the uid that owns the volume's root can differ, and git then refuses every command in the workspace. The setting protects multi-user hosts, and a sandbox has one user.
10. **The base image holds exactly `git`, `bash` and `ca-certificates`, and no `curl`, `rsync` or `tini`.** This is PRD 11's list. rsync is in the agent (14). Zombie reaping is the provider's `--init` (`sandbox_podman`), because an embedder's overlay-based image would not carry `tini` either. Later network tests can use bash's `/dev/tcp` or `git` instead of `curl`.
11. **The agent binary is owned by root with mode 0755 in both images.** The process running as uid 1000 cannot replace the agent that confines it, which matches 14's trust model ("the agent binary is trusted, workspace content is not").
12. **Tags are `X.Y.Z` plus a moving `X.Y` for non-prerelease versions, with no `latest`, and `DefaultImage` uses the exact `X.Y.Z`.** The tool-definition digest check (13) can fail when host and agent versions differ, so the default must be exact. `latest` would invite that skew. `X.Y` keeps PRD 11's `:0.6` example working for embedders who rebuild often.
13. **`ImageTag` refuses pseudo-versions, `(devel)` and build metadata, and `DefaultImage` then returns `ErrNoPublishedImage` instead of guessing a nearby release.** No image is published for those builds. A nearby release might fail the digest check with a confusing message, while an explicit error tells the embedder to set `Spec.Image`.
14. **The image references and `OverlayCopyLine` live in `sandbox` and are delivered here, for `sandbox_podman` to use.** The publishing pipeline and the code that names the images must agree. One constant pair plus an offline consistency test keeps them from drifting. The derived-image test proves that the exact line the podman error will print actually works.
15. **The container tests are opt-in through image variables and are run by `make sandbox-images-test` and the release workflow, not by `make check`.** Building the cgo image takes minutes and needs network access to pull base images. `make check` must stay offline (Goal 6). PRD 11's rule that tests are "skipped, not failed" without an engine still holds.
16. **The workflow tests before it publishes, using the runner's default docker builder, and only then sets up buildx for the multi-arch push.** The derived-image test needs a builder that sees locally built images. A buildx `docker-container` builder does not see them.
17. **A separate `verify` job pulls anonymously.** A package that only maintainers can pull has not been published for embedders. GHCR creates new packages as private, so the first release's `verify` fails until a maintainer makes the packages public, and the job can be re-run by itself.
18. **Docker tag computation is shell in the workflow, not `docker/metadata-action`, and actions are pinned to major version tags.** The mapping is "strip the `v`, add `X.Y` without a prerelease", which needs no third-party action. Commit-SHA pins cannot be verified offline, and a wrong one would break the workflow.
19. **psx's cgo variant must link with no `CGO_LDFLAGS_ALLOW`. If it does not, the work stops and an erratum records it.** 15 pins that no gate adds the variable. Adding it in the image build alone would make the image build different from the gate it is supposed to mirror.
20. **There is one root `.dockerignore`, excluding `.git` and `codesearch`.** Podman falls back to `.dockerignore` when there is no `.containerignore`, so one file serves both engines. With the version stamped explicitly (DD 4), `.git` is only dead weight in the context.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `13_sandbox_core` | depends on, modifies | The images package `sandbox/cmd/agentkit-sandboxd` at `sandbox.AgentPath`. The tests use `attach.Dial`, `stdioconn.New`, `Hello`'s facts (`Version`, `Workdir`, `OutlineBackend`, `Git`), `ProtocolMajor`/`ProtocolMinor` and the `Tools` digest check. This spec adds `buildver.stamp`, extends 13's README, architecture, configuration and CLI docs, and adds `sandbox/image.go` to package `sandbox`. |
| `14_sandbox_rsync_sync` | depends on | The agent carries the rsync protocol, which is why the base image ships no `rsync` binary and the test asserts its absence. |
| `15_sandbox_landlock_agent` | depends on | The base image's cgo build links psx's cgo variant, which 15 left to this scope. The harness starts the agent with `sandbox.AgentArgs`. The image layout (`/workspace`, `/home/agentkit`, tools under `/usr`) is shaped by 15's writable and read-only roots. The Landlock check reads `AgentInfo.Landlock`. |
| `09_repository_cut` | depends on | The new test-only package must pass `internal/policy`'s cross-target and host-cgo gates, and TS-09-20 (`make check`) must stay green. The direct-dependency allowlist must be unchanged. |

## Verified External API

**In-repository code (read):**
- `Makefile`: the `help` target greps `^[a-zA-Z_-]+:.*##`. The targets are `check: fmt vet lint test`, plus `doc` and `tidy`.
- `internal/policy/crosstarget_test.go`: `crossTargets`, `TestCrossTargetBuildAndVet` (cgo off) and `TestHostCgoBuildAndVet` (cgo on). The package holds only test files.
- `outline/treesitter.go` (`//go:build cgo`) and `outline/treesitter_nocgo.go` (`//go:build !cgo`).
- `tools/proc_unix.go`: `resolveShell` tries `/bin/bash`, then `bash`, then `sh`.
- `tools/tools.go`: `execute` takes `{"command": string, "timeout_s"?: int}`, and `write_file` takes `{"path", "content"}`.
- `go.mod`: `module github.com/agent-fox-dev/agentkit-go`, `go 1.27`. `LICENSE`: Apache-2.0. `.git/config`: remote `https://github.com/agent-fox-dev/agentkit-go.git`.

**From the earlier scopes** (specified in 13 and 15, not yet in the tree):
- `sandbox.AgentPath = "/usr/local/libexec/agentkit-sandboxd"`;
- `sandbox.ProtocolMajor` and `sandbox.ProtocolMinor`;
- `sandbox.AgentArgs(spec Spec) []string`;
- `sandbox.Spec{Workdir, Network, TTL, …}` and `sandbox.NetworkNone`;
- `sandbox.ToolOptions{}`;
- `sandbox.AgentInfo{Version, ProtocolMajor, ProtocolMinor, Workdir, OutlineBackend, Git, Landlock LandlockInfo{ABI, KernelABI, TCP, Note, Writable, ReadOnly}}`;
- `buildver.Version() string`;
- `stdioconn.New(r io.ReadCloser, w io.WriteCloser) net.Conn`;
- `attach.Dial(ctx, conn net.Conn, cfg attach.Config) (sandbox.Sandbox, error)`, with `attach.Config{ID, Spec, Destroy func(context.Context) error, Metadata, Provider}`;
- `Sandbox.Agent()`, `Tools(ctx, ToolOptions) ([]core.Tool, error)` and `Close(ctx) error`;
- the `--version` line `agentkit-sandboxd <version> (protocol M.N)`.

**Go toolchain** (Go 1.27). **Unverified**, because the source is outside the repository:
- `go build -ldflags "-X importpath.name=value"` sets a package-level string variable that has no initializer or a constant one;
- `-trimpath`, `-buildvcs=false` and `GOTOOLCHAIN=local`;
- `-s -w` leaves `runtime/debug.ReadBuildInfo` intact.

**Container engines.** **Unverified**:
- `podman|docker build -f FILE --target NAME -t REF --build-arg K=V [--platform P] CONTEXT`;
- `run --rm -i --name N --network none --env K=V --entrypoint PATH IMAGE ARGS…`;
- `image inspect --format '{{json .RootFS.Layers}}'`, and likewise `.Config.User`, `.Config.Entrypoint`, `.Config.Env`, `.Config.Volumes` and `.Config.Labels`;
- `rm -f N` and `rmi REF`;
- `docker buildx build --platform linux/amd64,linux/arm64 --push`;
- the automatic build arguments `BUILDPLATFORM`, `BUILDARCH`, `TARGETARCH` and `TARGETOS`, provided by BuildKit and by buildah;
- podman reads `.dockerignore` when no `.containerignore` exists.

**Debian and Docker Official Images.** **Unverified**:
- the image tags `debian:trixie-slim` and `golang:1.27-trixie`;
- the packages `git`, `bash`, `ca-certificates`, `crossbuild-essential-arm64` and `crossbuild-essential-amd64`;
- the compilers `aarch64-linux-gnu-gcc` and `x86_64-linux-gnu-gcc`;
- the CA bundle at `/etc/ssl/certs/ca-certificates.crt`;
- `useradd`, `groupadd` and `git config --system`.

**GitHub Actions.** **Unverified**:
- `actions/checkout@v4`;
- `actions/setup-go@v5` with `go-version-file`;
- `docker/setup-qemu-action@v3` and `docker/setup-buildx-action@v3`;
- `docker/login-action@v3` with `registry`, `username` and `password`;
- the contexts `GITHUB_REF_NAME`, `GITHUB_SHA`, `github.actor` and `secrets.GITHUB_TOKEN`;
- the job-level `permissions: packages: write`;
- GHCR creates a new package as private and links it to the repository through `org.opencontainers.image.source`.

**`kernel.org/pub/linux/libs/security/libcap/psx`** is indirect and never called here. **Unverified**: its cgo variant links with `-Wl,-wrap,pthread_create`. Whether Go's cgo flag allowlist accepts that flag without `CGO_LDFLAGS_ALLOW` is the open condition DD 19 handles.
