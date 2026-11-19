# Releasing

What a release of AgentKit consists of and how to cut one. It is written for
maintainers.

## Contents

- [What is released](#what-is-released)
- [Cutting a release](#cutting-a-release)
- [The last release with sandboxes](#the-last-release-with-sandboxes)

## What is released

| Artifact | How it is published |
|---|---|
| The Go modules | Git tags. Go resolves the root module from `vX.Y.Z` and each nested module from a tag with its directory as prefix: `codesearch/vX.Y.Z` and `mcpserve/vX.Y.Z`. |

Nothing else is published: no binaries, images, archives, formulae or charts.

## Cutting a release

1. Merge everything for the release into `main` and make sure CI is green.
   A maintainer runs `make check-release` on the release commit.
2. Tag the release commit for the root module and the nested modules, and
   push the tags together:

   ```bash
   git tag v0.10.0
   git tag codesearch/v0.10.0
   git tag mcpserve/v0.10.0
   git push origin v0.10.0 codesearch/v0.10.0 mcpserve/v0.10.0
   ```

## The last release with sandboxes

AgentKit no longer creates sandboxes
([ADR 08](adr/08-leave-sandboxes-to-the-platform-that-runs-them.md)).
`v0.9.1`, on commit `f421f0b`, is the last release whose tree holds the
sandbox module, its gateway and its image. That module was never tagged, so a
program that still needs it pins it by commit:
`go get github.com/agent-fox-dev/agentkit-go/sandbox@f421f0b`, which Go
resolves to a pseudo-version.

`.github/workflows/build-sandbox-containers.yaml` is left from the sandbox
image and fails, since the image's Containerfile is gone; ADR 08 records why
it stays.
