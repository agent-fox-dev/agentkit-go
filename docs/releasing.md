# Releasing

A release publishes the sandbox image, `quay.io/agentfox/agentkit-sandbox`.
Nothing else is released by automation: no binaries, archives or
formulae. The workflow is `.github/workflows/sandbox-images.yml`.

The sandbox gateway, `agentkit-gateway`, is not published: no binary, image
or chart. An operator builds it from a checkout with
`make sandbox-gateway AGENTKIT_VERSION=vX.Y.Z`, which stamps the version
its `--version` reports (`-ldflags -X …/sandbox/internal/buildver.stamp`);
without `AGENTKIT_VERSION` it reports `(devel)`. The version is
informational: a client picks the sandbox image by its own version, not the
gateway's.

## Cutting a release

Push the version tag at the release commit:

```bash
git tag v0.6.0
git tag sandbox/v0.6.0
git push origin v0.6.0 sandbox/v0.6.0
```

`vX.Y.Z` triggers the workflow. `sandbox/vX.Y.Z` is the tag Go resolves for
the nested module `github.com/agent-fox-dev/agentkit-go/sandbox` at
`vX.Y.Z`; an embedder's binary reports that version, and
`sandbox.DefaultImage()` turns it into the image reference, so both tags are
pushed together
(`docs/errata/16_sandbox_images.md`). The workflow's `v*` trigger does not
match `sandbox/v*`, so the second tag starts nothing.

Only tags starting with `v` start the workflow, and such a tag must match
`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`;
any other `v*` tag fails the workflow before it builds.

## What the workflow does

1. The `images` job builds the image on the runner's default docker
   builder with `AGENTKIT_VERSION` set to the tag, and runs
   `make sandbox-images-test` against it. A failing test ends the run
   before anything is published.
2. It then sets up QEMU and buildx, logs in to `ghcr.io` with the workflow's
   `GITHUB_TOKEN`, and pushes it for linux/amd64 and linux/arm64.
3. The `verify` job pulls every published tag for both platforms without
   logging in, and checks that `--version` prints a line starting with
   `agentkit-sandboxd vX.Y.Z ` (followed by `(protocol M.N)`).

## Tags

| Version | Tags |
|---|---|
| `v1.2.3` | `1.2.3` and `1.2` |
| `v1.2.3-rc.1` | `1.2.3-rc.1` only |

There is no `latest`. `X.Y` moves with each patch release. A re-run of the
workflow for a tag rebuilds and re-pushes that tag, picking up the base
images' security updates, so an embedder who needs an immutable image pins
its digest (`…@sha256:…`).

## The first release: making the packages public

GHCR creates a new package as private, linked to the repository through
the `org.opencontainers.image.source` label. On the first release the
`verify` job therefore fails with a pull error naming the reference. A
maintainer then opens the package's settings on GitHub (`agentkit-sandbox`),
changes its visibility to public, and re-runs the `verify` job alone; it takes its inputs from the
`images` job's outputs, so nothing is rebuilt.

## Re-runs and dry runs

- **Re-run** the whole workflow from the Actions page to rebuild a tag; re-run
  only `verify` to re-check what was published.
- **Dry run:** start the workflow by hand (`workflow_dispatch`) on any branch.
  It builds the image and runs the image tests with an empty version, and
  never logs in, pushes or verifies.

## Local builds

```bash
make build-containers                        # tags VERSION (git describe) and latest
make build-containers VERSION=v0.6.0
```

`make build-containers` uses podman and builds
`containers/agentkit-sandbox/Containerfile`; it needs network access to
pull the UBI base images from `registry.redhat.io`. The make variables are
listed in [`configuration.md`](configuration.md#sandbox-images). It is not
part of `make check`, which builds no image.
