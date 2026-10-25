# Development

How to build, test and change AgentKit. For how the code is organised inside,
read [architecture.md](architecture.md); for publishing, read
[releasing.md](releasing.md).

## Contents

- [Prerequisites](#prerequisites)
- [Repository layout](#repository-layout)
- [Modules](#modules)
- [Make targets](#make-targets)
- [The quality gate](#the-quality-gate)
- [Tests](#tests)
  - [Provider conformance](#provider-conformance)
  - [Live provider tests](#live-provider-tests)
  - [Container, Landlock and gateway tests](#container-landlock-and-gateway-tests)
  - [Other opt-in tests](#other-opt-in-tests)
- [Generated files](#generated-files)
- [Workflow](#workflow)
- [Writing documentation](#writing-documentation)

## Prerequisites

| Tool | Needed for |
|---|---|
| Go 1.27 or later | Everything. Every `go.mod` says `go 1.27`. |
| `make` | The targets below. |
| A C compiler (cgo) | Optional: tree-sitter outlines for non-Go languages. Without cgo every package still builds, and those languages have no outline. |
| `golangci-lint` | Optional: `make lint` skips it when it is not installed. Configuration is in `.golangci.yml`. |
| Podman or Docker | Only for the container tests and `make build-containers`. |
| `protoc` | Only for `make proto`. |

No API key and no network access are needed for `make check`: every test that
talks to a model uses a scripted provider or a local fake of the vendor's API.

## Repository layout

```
*.go                    the root package: the agent loop (agentkit.Config, agentkit.New)
                        and the runner (agentkit.Toolset, agentkit.NewRunner)
core/ schema/ wire/     shared types, JSON Schema values, bounded parsing of untrusted JSON
provider/               the anthropic, openrouter, ollama and faux providers
catalog/                the embedded model catalog and its tiers
workspace/              containment, ignore rules and the directory walk
tools/ guard/ outline/  the built-in tools, the shell guard, source outlines
codemode/ mcp/ prompt/  code mode, the MCP client pool, system prompt assembly
sandbox/                nested module: sandbox API, protocol, agent and providers
sandbox/gatewayd/       nested module: the agentkit-gateway server
mcpserve/               nested module: serving a Runner's tools over MCP
codesearch/             nested module: zoekt-backed code search
examples/               runnable examples (some are nested modules)
containers/             the sandbox image's Containerfile
internal/               test-only packages: build gates, conformance suites, helpers
testdata/               golden files and fixtures
docs/                   documentation; docs/generated/ is generated
.specs/                 specifications to implement; .specs/archive/ holds old ones
```

Tests live next to the code as `*_test.go`.

## Modules

The root module is `github.com/agent-fox-dev/agentkit-go`. Parts with heavy
dependencies are nested modules, so an embedder that does not use them does
not link their dependencies:

| Module | Path |
|---|---|
| root | `.` |
| code search | `codesearch` |
| sandboxes | `sandbox` |
| gateway server | `sandbox/gatewayd` |
| MCP serving | `mcpserve` |
| examples with their own module | `examples/sandbox`, `examples/gateway`, `examples/harness`, `examples/indexedsearch` |

Each nested module requires its siblings through `replace` directives, so a
checkout always builds against itself. The list lives in `MODULES` in the
`Makefile`; `make test`, `make vet`, `make lint`, `make build` and `make tidy`
run in each of them.

## Make targets

`make help` lists them all.

| Target | Does |
|---|---|
| `make check` | `fmt`, `vet`, `lint` and `test-short`: the fast check. Run it before every commit. |
| `make check-release` | `fmt`, `vet`, `lint` and the full `test`, slow gates included. Run by a maintainer before a release or merge; it takes about ten minutes. |
| `make test-short` | `go test -short ./...` in every module. |
| `make test` | `go test ./...` in every module, including the slow gates (cross-target builds and vet, the host cgo build, nested `make` runs). |
| `make build` | `go build ./...` in every module. |
| `make coverage` | The root module's tests with a coverage report. |
| `make fmt` | `gofmt -l -w .` |
| `make vet` | `go vet` in every module. |
| `make lint` | `golangci-lint` in every module; skipped when not installed. |
| `make tidy` | `go mod tidy` in every module. |
| `make doc` | Regenerate the package reference in `docs/generated/`. |
| `make proto` | Regenerate the sandbox protocols' Go code (needs `protoc`). |
| `make clean` | Remove `build/` and the coverage profile. |
| `make build-containers` | Build the sandbox image with Podman. |
| `make sandbox-podman-test` | Build the image and run the Podman provider's integration tests. |
| `make sandbox-landlock-test` | Run the Landlock provider's tests verbosely, including a real public clone. |
| `make sandbox-gateway` | Build `build/bin/agentkit-gateway`; `AGENTKIT_VERSION=vX.Y.Z` stamps its version. |
| `make sandbox-gateway-test` | Build the image and run the gateway's Podman tests. |
| `make provider-live-test` | One real tool loop on each provider you have credentials for. Costs money. |

Only `check`, `check-release` and their parts run in the default gates; the
container, Landlock, gateway and live targets are opt-in.

## The quality gate

`make check` is the gate for every change: formatting, vet, lint and the short
tests of every module. While you work, narrow runs are fine:

```bash
go test ./tools/...                      # one package of the root module
(cd sandbox && go test ./podman/...)     # a package of a nested module
go test -short -run TestRunner ./...     # one test by name
```

`make check-release` adds the slow gates in `internal/policy`: the four
cross-target builds and vet (linux/amd64, linux/arm64, darwin/arm64 and
windows/amd64 with cgo off), the host build with cgo on, and nested `make`
runs. It is a manual step for maintainers before a release or a merge;
contributors and coding agents run `make check` only.

`internal/policy` also enforces rules the code relies on: the
direct-dependency allowlist ([DEPS.md](DEPS.md)), that the library opens no
listening socket outside the nested server modules, and an exported-API golden
(`internal/policy/testdata/exported-api.golden`) for the root, `core`, `tools`,
`codemode`, `workspace`, `guard`, `provider/faux`, `sandbox`, `codesearch` and
`mcpserve`. When you change one of those surfaces on purpose, regenerate the
golden and commit it with the change.

## Tests

- Unit and property tests sit beside their package.
- `provider/faux` scripts a model offline; the root tests and the examples run
  against it.
- `testdata/golden/` holds the exact request bodies sent to Anthropic. It pins
  regressions, not the vendor's behaviour.
- `internal/toolconformance` runs one table of cases against `tools.For` and
  against sandbox proxy tools, so the two stay interchangeable.

### Provider conformance

`internal/providerconformance` runs the same seven logical turns through each
of the three providers and requires one canonical outcome: `text`,
`tool_call`, `tool_call_stop`, `thinking`, `cut_off`, `error_status` and
`error_in_stream`, 21 subtests named `<provider>/<case>`. Each provider talks
to a fake of its vendor's native API on a loopback test server, built with the
standard library only, so the providers are checked against the wire format
rather than against themselves. It never reads the process environment, so
real keys on your machine change nothing. It runs under `-short`:

```bash
go test -short -run 'TestProviderConformance.*/ollama/thinking' ./internal/providerconformance
```

### Live provider tests

`make provider-live-test` runs one real tool loop on each provider whose
prerequisites are present. It is

```bash
AGENTKIT_LIVE=1 go test -count=1 -v -timeout 30m -run TestLive ./internal/providerconformance
```

and no other target runs it. `TestLive` skips under `-short` and unless
`AGENTKIT_LIVE` is exactly `1`, so having a key or a running Ollama server
never starts a paid run by accident. Each provider is a subtest that skips,
naming what is missing, unless:

| Provider | Prerequisites |
|---|---|
| `anthropic` | `anthropic.Resolve` accepts the process environment: the direct API, Vertex AI or Bedrock. |
| `openrouter` | `OPENROUTER_API_KEY` is set. |
| `ollama` | An Ollama server answers at `OLLAMA_HOST` (or the default) within 2 seconds and has the `STANDARD` tier's model. |

A ready subtest writes a file holding a random word into a temporary
workspace and asks the `STANDARD` tier to read it with `read_file` and answer
with the word, within 6 turns, $0.50 and 10 minutes. It checks the answer, the
resolved provider, model and effort, the token counts and the cost (positive
on `anthropic` and `openrouter`, zero on `ollama`), and logs them.

### Container, Landlock and gateway tests

These need a container engine or a recent Linux kernel, and skip without one or
under `-short`:

| Target | Runs | Environment it sets |
|---|---|---|
| `make sandbox-podman-test` | `sandbox/podman`'s integration tests against a freshly built image. The `GitRepo` test clones `AGENTKIT_SANDBOX_GIT_URL` at depth 1; the micro-VM test runs only with `AGENTKIT_SANDBOX_RUNTIME=krun`. | `AGENTKIT_SANDBOX_BASE_IMAGE`, `AGENTKIT_SANDBOX_IMAGE_VERSION`, `AGENTKIT_CONTAINER` |
| `make sandbox-landlock-test` | `sandbox/landlock`'s tests verbosely, with a real clone of `AGENTKIT_SANDBOX_GIT_URL`. Its offline and Linux tests also run in `make check`, skipping below Landlock ABI 4. | `AGENTKIT_SANDBOX_GIT_URL` |
| `make sandbox-gateway-test` | `sandbox/gatewayd`'s Podman tests. The gateway's offline tests (an in-process server over `local`) run in `make check`. | `AGENTKIT_SANDBOX_BASE_IMAGE`, `AGENTKIT_CONTAINER` |

`AGENTKIT_SANDBOX_GIT_URL` defaults to
`https://github.com/agent-fox-dev/agentkit-go`. The image tests in
`sandbox/images` run against the image named by `AGENTKIT_SANDBOX_BASE_IMAGE`.

### Other opt-in tests

| Variable | Turns on |
|---|---|
| `AGENTKIT_BASH_DIFF=1` | `TestCommandsAgainstBash`: checks the shell guard's reading of command lines against a real `bash`. |

## Generated files

Some files are generated and committed. Never edit them by hand:

| Files | Regenerate with | When |
|---|---|---|
| `docs/generated/*.txt` | `make doc` | An exported identifier or a package doc comment changes. |
| `sandbox/internal/sandboxpb/*.pb.go` and the `*.proto.sha256` hashes | `make proto` | `sandbox.proto` or `gateway.proto` changes. It installs pinned `protoc-gen-go` and `protoc-gen-go-grpc` into `build/bin`; tests check the recorded hashes offline. |
| `internal/policy/testdata/exported-api.golden` | `AGENTKIT_UPDATE_API_GOLDEN=1 go test -run TestSandboxLayoutAndAPI ./internal/policy` | One of the recorded packages' exported API changes on purpose; review the golden's diff. |

## Workflow

- Branch from `main` as `feature/<descriptive-name>`; never commit to `main`.
- Use conventional commits: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`,
  `chore:`.
- One coherent change per branch. Run `make check` before committing, then
  push and open a pull request.
- New work starts from a specification in `.specs/NN_name/` (`prd.md`,
  `requirements.json`, `test_spec.json`, `tasks.json`). Product requirements
  live in `docs/prd/`, and the decisions the code follows, where it differs
  from a spec among them, in the ADRs under [`docs/adr/`](adr/).
- Coding agents also follow [`AGENTS.md`](../AGENTS.md) and
  [`.specs/steering.md`](../.specs/steering.md).

## Writing documentation

The documentation rules are in the **Documentation Style** section of
[`.specs/steering.md`](../.specs/steering.md). In short: a README explains
what the thing is and the problem it solves, shows a short example, and points
to further documentation; implementation details belong in `docs/`; and a
long reference document opens with a contents index. When a change touches
the public API, a command, a configuration option or the architecture, update
[api.md](api.md), [cli.md](cli.md), [configuration.md](configuration.md) or
[architecture.md](architecture.md) in the same change.
