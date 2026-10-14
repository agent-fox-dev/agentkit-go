# Releasing

A release publishes the two sandbox images: `quay.io/agentfox/agentkit-sandbox`
(the base image) and `quay.io/agentfox/agentkit-sandboxd` (the overlay
image). Nothing else is released by automation: no binaries, archives or
formulae. The workflow is `.github/workflows/build-sandbox-containers.yaml`,
which runs the reusable `.github/workflows/inc-build.yaml` once per image.

The sandbox gateway, `agentkit-gateway`, is not published: no binary, image
or chart. An operator builds it from a checkout with
`make sandbox-gateway AGENTKIT_VERSION=vX.Y.Z`, which stamps the version
its `--version` reports (`-ldflags -X …/sandbox/internal/buildver.stamp`);
without `AGENTKIT_VERSION` it reports `(devel)`. The version is
informational: a client picks the sandbox image by its own version, not the
gateway's.

## Cutting a release

Push the version tags at the release commit:

```bash
git tag v0.6.0
git tag sandbox/v0.6.0
git push origin v0.6.0 sandbox/v0.6.0
```

`sandbox/vX.Y.Z` is the tag Go resolves for the nested module
`github.com/agent-fox-dev/agentkit-go/sandbox` at `vX.Y.Z`; an embedder's
binary reports that version, and `sandbox.DefaultImage()` and
`sandbox.OverlayImage()` turn it into the image references, so both tags
are pushed together (`docs/errata/16_sandbox_images.md`).

Then start **agentkit sandbox containers build** from the Actions page
(`workflow_dispatch`) with the `vX.Y.Z` tag as the ref. The tag-push
trigger is commented out, so pushing the tags alone builds nothing.

## What the workflow does

For each image, `inc-build.yaml` runs three jobs:

1. **prepare** reads the version with `git describe --tags --match 'v[0-9]*'`,
   which skips the `sandbox/v*` module tags. On a `vX.Y.Z` tag it yields
   `vX.Y.Z`; between tags it yields `vX.Y.Z-N-g<commit>`; with no tag at
   all, the short commit. The agent is stamped with that version as is
   (`dev` when there is no tag); the image tag is the version without its
   `v`.
2. **build** builds the image natively on an amd64 and an arm64 runner,
   after logging in to `registry.redhat.io` (for the UBI base images) and
   to the target registry, and pushes `<version>-amd64` and
   `<version>-arm64`. It passes `AGENTKIT_VERSION`, `AGENTKIT_REVISION`
   (the short commit) and `AGENTKIT_BUILD_TIME` as build arguments: they
   are stamped into the agent and fill the `version` and `revision`
   labels. The Containerfile's labels are the image's labels.
3. **manifest** joins the two into a multi-arch `<version>` manifest, adds
   the extra tags below, and attaches a build-provenance attestation.

The repository variables `REGISTRY` and `REGISTRY_ORG` name the target
(`quay.io` and `agentfox`); the secrets `RH_ACCOUNT_USERNAME`,
`RH_ACCOUNT_PASSWORD`, `REGISTRY_USERNAME` and `REGISTRY_TOKEN` hold the
credentials.

The workflow runs no tests against the images. Build and test them locally
first (below).

## Tags

| Run on | Tags on each repository |
|---|---|
| the tag `v1.2.3` | `1.2.3` |
| the tag `v1.2.3-rc.1` | `1.2.3-rc.1` |
| `main` | `<version>` and `latest` |
| a `release-*` branch | `<version>` and the branch name |

There is no moving `X.Y` tag. A re-run rebuilds and re-pushes its tags, so
an embedder who needs an immutable image pins its digest (`…@sha256:…`).

## Local builds

```bash
make build-containers                        # tags VERSION (git describe) and latest
make build-containers VERSION=v0.6.0
make sandbox-podman-test                     # build, then the podman provider's tests
```

`make build-containers` uses podman and builds
`containers/agentkit-sandbox/Containerfile` and
`containers/agentkit-sandboxd/Containerfile`; it needs a `podman login` to
`registry.redhat.io` to pull the UBI base images. The image tests in
`sandbox/images` run against the built images when
`AGENTKIT_SANDBOX_BASE_IMAGE` and `AGENTKIT_SANDBOX_OVERLAY_IMAGE` name them.
The make variables are listed in
[`configuration.md`](configuration.md#sandbox-images). None of these targets
is part of `make check`, which builds no image.
