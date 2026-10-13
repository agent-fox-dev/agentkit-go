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
