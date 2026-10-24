# Releasing

What a release of AgentKit consists of, and how the one published artifact —
the sandbox image — is built and pushed.

## Contents

- [What is released](#what-is-released)
- [Cutting a release](#cutting-a-release)
- [The image workflow](#the-image-workflow)
- [Image tags](#image-tags)
- [Registry and secrets](#registry-and-secrets)
- [Building the image locally](#building-the-image-locally)
- [The gateway](#the-gateway)

## What is released

| Artifact | How it is published |
|---|---|
| The Go modules | Git tags. Go resolves the root module from `vX.Y.Z` and each nested module from a tag with its directory as prefix. |
| The sandbox image `quay.io/agentfox/agentkit-sandbox` | The GitHub Actions workflow below. |

Nothing else is published: no binaries, archives, formulae or charts.

## Cutting a release

1. Merge everything for the release into `main` and make sure CI is green.
   A maintainer runs `make check-release` on the release commit.
2. Tag the release commit for the root module and the nested modules, and
   push the tags together:

   ```bash
   git tag v0.6.0
   git tag sandbox/v0.6.0
   git tag mcpserve/v0.6.0
   git push origin v0.6.0 sandbox/v0.6.0 mcpserve/v0.6.0
   ```

   The root and `sandbox` tags must match: an embedder's binary reports the
   sandbox module's version, and `sandbox.DefaultImage()` turns that version
   into the image tag it pulls (`v0.6.0` → `:0.6.0`).
3. Publish the image for the tag. The workflow runs on every push to `main`,
   but it only uses a clean version when the commit it builds is exactly at a
   tag, so after pushing the tags start it by hand: **Actions → agentkit
   sandbox containers build → Run workflow** on `main`.
4. Check that `quay.io/agentfox/agentkit-sandbox:0.6.0` exists for both
   platforms.

## The image workflow

`.github/workflows/build-sandbox-containers.yaml` starts on a push to `main`
and on `workflow_dispatch`, and calls the reusable
`.github/workflows/inc-build.yaml` with `container_name: agentkit-sandbox`:

| Job | Does |
|---|---|
| `prepare` | Works out the version with `git describe --tags`, without the leading `v`: the tag itself when the commit is exactly at one (a release), otherwise `<tag>-<n>-g<sha>`, or the short commit hash when there is no tag. |
| `build` | Builds `containers/agentkit-sandbox/Containerfile` natively on an amd64 and an arm64 runner, and pushes `<version>-amd64` and `<version>-arm64`, with a registry build cache per platform. |
| `manifest` | Joins the two into a multi-platform `<version>` tag, adds the extra tags below, and attaches a build-provenance attestation to the image. |

The workflow passes no build arguments, so the agent in a CI-built image is
stamped with the Containerfile's default version, `dev`; a locally built image
(`make build-containers`) is stamped with `git describe`. The image tests in
`sandbox/images` are not run by the workflow; run `make sandbox-podman-test`
before a release to exercise the image.

## Image tags

| Built from | Tags |
|---|---|
| `main`, commit at tag `v1.2.3` | `1.2.3` and `latest` |
| `main`, between tags | `1.2.3-4-gabc1234` and `latest` |
| a `release-*` branch (by hand) | the version and the branch name |
| any other branch (by hand) | the version only |

`latest` moves with every push to `main`. A re-run of the workflow rebuilds
and re-pushes its tags, picking up base-image security updates, so an
embedder who needs an immutable image pins its digest (`…@sha256:…`).

## Registry and secrets

The workflow reads the registry from repository variables and logs in with
repository secrets:

| Name | Kind | Used for |
|---|---|---|
| `REGISTRY`, `REGISTRY_ORG` | variables | The image name, `<REGISTRY>/<REGISTRY_ORG>/agentkit-sandbox` (`quay.io/agentfox`). |
| `REGISTRY_USERNAME`, `REGISTRY_TOKEN` | secrets | Pushing to that registry. |
| `RH_ACCOUNT_USERNAME`, `RH_ACCOUNT_PASSWORD` | secrets | Pulling the UBI base images from `registry.redhat.io`. |

## Building the image locally

```bash
make build-containers                  # tags $(VERSION) (git describe) and latest
make build-containers VERSION=v0.6.0
```

It builds with Podman and needs access to `registry.redhat.io` for the UBI
base images. The make variables (`CONTAINER_REGISTRY`, `SANDBOX_IMAGE`,
`SANDBOX_IMAGE_TAG`, `VERSION`) are listed under
[Sandbox images](configuration.md#sandbox-images). It is not part of
`make check`.

## The gateway

`agentkit-gateway` is not published. An operator builds it from a checkout of
the release tag:

```bash
make sandbox-gateway AGENTKIT_VERSION=v0.6.0
```

which stamps the version its `--version` reports; without `AGENTKIT_VERSION` it
reports `(devel)`. The version is informational: a client picks the sandbox
image by its own version, not the gateway's. See [cli.md](cli.md#agentkit-gateway).
