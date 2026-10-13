# Erratum: spec 16 `sandbox_images`

The spec was written before spec 14 made `sandbox/` a nested Go module
(`docs/errata/14_sandbox_rsync_sync.md`, D-2). These are the places the
delivered code differs from its text, and why.

## The agent is built from the sandbox module (D-1)

The spec builds `./sandbox/cmd/agentkit-sandboxd` from the root module and
runs `go test ./sandbox/images/` from the repository root. Both fail there:
the root module does not contain the sandbox packages. So:

- the Containerfile copies both `go.mod`/`go.sum` pairs, downloads modules
  in `sandbox/`, and builds `./cmd/agentkit-sandboxd` there; the build
  context is still the repository root, which the sandbox module's
  `replace ../` needs;
- `make sandbox-images-test` runs `go test -count=1 -v ./images/` in
  `sandbox/`;
- TS-16-3 builds the command from the sandbox module's root, whose
  `go list -m` is `github.com/agent-fox-dev/agentkit-go/sandbox`, and the
  offline checks that pin package paths read them relative to that module.

The `-X` path is the package's import path, which is unchanged:
`github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp`.

## buildver reports the sandbox module's version (D-2)

Since spec 14, `buildver.Version()` reads the build info of the module it
lives in, `github.com/agent-fox-dev/agentkit-go/sandbox`, not the root
module. TS-16-1's independent oracle reads that module's entry.

## A nested module is released with a prefixed tag (D-3)

`DefaultImage` and `OverlayImage` use the version `buildver.Version()`
reports: an embedder's dependency on `github.com/agent-fox-dev/agentkit-go/sandbox`.
Go resolves that module's `vX.Y.Z` from the git tag `sandbox/vX.Y.Z`, not
`vX.Y.Z`. The release workflow still publishes images on the `vX.Y.Z` tag
(its `v*` trigger does not match `sandbox/v*`), so a release pushes both
tags at the same commit; `docs/releasing.md` says so.

## make sandbox-images restates the version and revision labels

16-REQ-6.2 lists the arguments of the two builds. `make sandbox-images`
also passes `--label org.opencontainers.image.version=$(AGENTKIT_VERSION)`
and `--label org.opencontainers.image.revision=$(AGENTKIT_REVISION)`
(`SANDBOX_LABELS` in the Makefile). With podman, buildah's layer cache
reused the overlay stage's `LABEL` step from an earlier build after
`AGENTKIT_REVISION` changed: the overlay stage has no `RUN`, so the build
arguments never enter its cache key, and TS-16-42 found the overlay
labelled with the previous commit while the base carried the current one.
The command-line labels apply to the final image whatever the cache
holds; the Containerfile's labels are unchanged, and the release workflow's
BuildKit builds recompute them anyway.

## TS-16-49 finds the stamp in the binary, not in its build info

TS-16-49 expects `debug/buildinfo` to report `-ldflags` containing
`buildver.stamp=v9.8.7`. With `-trimpath`, Go leaves `-ldflags` out of the
build info (go.dev/issue/52372); a Go 1.27.1 build of the agent with both
flags records `-trimpath=true` and no `-ldflags`. The test still checks
GOOS, GOARCH, CGO_ENABLED and -trimpath through the build info, and asserts
the stamp by finding `v9.8.7` in the cross-built binary itself
(`sandbox/images/overlay_image_test.go`). It would use the build-info
setting if a future Go recorded it.
