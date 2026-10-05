# Erratum: codesearch's forbidden-import check covers direct imports only

**Relates to:** spec 03 (`.specs/03_indexed_code_search`) — 03-REQ-9.3,
03-REQ-10.1, PRD §8 and Design Decision 17; TS-03-63.
**Status:** accepted by the project owner in
[agent-fox-dev/agentkit-go#48](https://github.com/agent-fox-dev/agentkit-go/issues/48).

## What the spec said

PRD §8 listed, among the conditions that end the spec as a no-go, `codesearch`
"needing to import a package that pulls in gRPC, Prometheus or an HTTP server
(only the indexing, query and search packages are allowed)". 03-REQ-10.1 and the
Policy section called for "a forbidden-import check **over `go list -deps`**".
Design Decision 17 added that loosening the forbidden list after seeing the
graph would defeat the gate.

## What was delivered

`TestForbiddenImports` in `codesearch/policy_test.go` inspects only the packages
of the `codesearch` module and checks their **direct** imports. It does not walk
`go list -deps`. `codesearch/README.md` reported "Forbidden Imports
(03-REQ-9.3): PASS" on that reading without recording that it differed from the
spec.

The two cannot both hold. The packages the spec allows — `zoekt/index` and
`zoekt/search` — import those stacks themselves, so a check over the transitive
graph could never pass, and zoekt could not be used at all. `cd codesearch && go
list -deps ./...` includes, among others:

| Module | Version | Packages in the graph |
|---|---|---|
| `google.golang.org/grpc` | v1.82.1 | the root package and 63 sub-packages (`balancer`, `credentials`, `internal/transport`, `status`, …) |
| `github.com/grpc-ecosystem/go-grpc-middleware/v2` | v2.3.3 | the root package |
| `github.com/prometheus/client_golang` | v1.20.5 | `prometheus`, `prometheus/promauto` |
| `github.com/prometheus/client_model` | v0.6.1 | `go` |
| `github.com/prometheus/common` | v0.62.0 | `expfmt`, `model` |
| `github.com/prometheus/procfs` | v0.15.1 | the root package and its internal packages |
| `github.com/getsentry/sentry-go` | v0.31.1 | the root package and its internal packages |
| `github.com/sourcegraph/zoekt` | the pinned pseudo-version | `grpc/propagator`, `grpc/protos/zoekt/webserver/v1` |

## The decision

The project owner chose to **accept the transitive graph** rather than record a
no-go. Per the owner's ruling on issue #48, the strict stdlib-only import policy
is no longer a hard requirement for the project
([`dependency_policy.md`](dependency_policy.md)), and the codesearch module is
already a nested module so that no embedder that does not import it carries
zoekt's graph.

The forbidden-import check is therefore **direct-imports-only, on purpose**:

- No package of the `codesearch` module may directly import
  `google.golang.org/grpc`, `github.com/prometheus/…`,
  `github.com/grpc-ecosystem/…` or `net/http/httptest`. This is what
  `TestForbiddenImports` checks, and `TestForbiddenImportsDetectsSynthetic`
  proves the matcher can see a violation.
- The transitive packages in the table above are accepted as the cost of using
  zoekt's index and search packages.
- Moving off zoekt, or narrowing what the module links, would be a new decision
  and not a repair of this one.

## What this means for embedders

An embedder that opts into `codesearch` links the packages above into its
binary. One that does not import `codesearch` is unaffected: the root module's
`go list -deps ./...` does not descend into the nested module. The gate's purpose
— keeping a server stack out of every embedder — is therefore met at the
module boundary, not by the import check.

## What changed

- `.specs/03_indexed_code_search/requirements.json`, `prd.md` and
  `test_spec.json` word 03-REQ-9.3, 03-REQ-10.1, PRD §8, the Policy bullet,
  DD17 and TS-03-63 as a direct-imports check.
- `codesearch/policy_test.go` states that scope and cites this erratum; the test
  logic and names are unchanged.
- `codesearch/README.md`'s "Forbidden Imports" section says the same and lists
  the accepted packages.
- `docs/prd/05-add-an-indexed-code-search-module.md` §6 points here.
