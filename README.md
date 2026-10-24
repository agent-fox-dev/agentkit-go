# agentkit-go

AgentKit is a Go library for building AI coding agents. It gives an agent the
**hands** it needs to work on a real codebase — tools to read, search, edit and
run code, a guard that decides which shell commands may run, and sandboxes to
run those tools in — together with a small **agent loop** that drives a model
over them on Anthropic, OpenRouter or Ollama.

## Why AgentKit

Letting a language model work on a repository takes much more than an API
call. Every agent has to answer the same questions:

- **Which files may it touch?** File tools must stay inside the project, honour
  `.gitignore`, and cope with large files and outputs.
- **Which commands may it run?** An unattended agent with an unrestricted shell
  is one bad completion away from damage.
- **Where does it run?** Untrusted, model-written code belongs in an isolated
  environment, not on the developer's machine.
- **How is the run bounded?** Turns, cost and wall-clock time all need limits.

AgentKit answers them once, as a library:

| Need | What AgentKit provides |
|---|---|
| Work on a codebase | Eleven built-in tools — read, write, edit, list, find, search, outline, find symbols, find references, run commands — contained to one workspace and its ignore rules. |
| Stay in control of the shell | A shell guard that reads every command on a line and allows only the programs you list. An agent with a shell tool and no guard is refused at construction. |
| Isolate the work | Sandboxes that run the same tools in a container (Podman or Docker), under Linux Landlock, or on a remote gateway, with the files synced in and out. |
| Fewer round trips | Code mode: the model writes one short Starlark script that calls many tools, instead of one tool call per turn. |
| Connect other tools | An MCP client that imports the tools of any MCP server, and an MCP server that serves AgentKit's tools to another harness such as Claude Code. |
| Run the model | A minimal agent loop with turn, cost and time limits, an event stream, and providers for Anthropic (direct, Vertex AI, Bedrock), OpenRouter and Ollama. |

Each part works on its own: you can use the tools and the guard in your own
agent loop, or AgentKit's loop with tools of your own.

## Example

An agent that answers a question about the current directory, allowed to run
only `go` and `git`:

```go
package main

import (
	"context"
	"fmt"
	"log"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func main() {
	ws, err := workspace.Open(".", workspace.Options{})
	if err != nil {
		log.Fatal(err)
	}
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		log.Fatal(err)
	}
	agent, err := agentkit.New(agentkit.Config{
		Provider: "anthropic", // or "openrouter", "ollama"
		Model:    "STANDARD",  // a tier (SIMPLE, STANDARD, ADVANCED) or a model id
		Toolset: agentkit.Toolset{
			Tools: ts,
			Guard: guard.Allow("go", "git"),
		},
		MaxTurns:   20,
		MaxCostUSD: 1.00,
	})
	if err != nil {
		log.Fatal(err) // e.g. a missing ANTHROPIC_API_KEY, reported by name
	}
	res, err := agent.Run(context.Background(), "Which package defines the shell guard?")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.FinalText())
}
```

The provider reads its credential from the environment: `ANTHROPIC_API_KEY`
(or Vertex AI / Bedrock settings), `OPENROUTER_API_KEY`, or `OLLAMA_HOST` for a
local Ollama server.

To see the loop work with no API key at all, run the offline demo, which uses a
scripted model:

```bash
go run ./examples/agentdemo
```

## Installation

AgentKit needs Go 1.27 or later and runs on Linux and macOS (Windows is best
effort).

```bash
go get github.com/agent-fox-dev/agentkit-go
```

Three optional parts are separate Go modules, so you only pay for their
dependencies when you use them:

| Module | Adds |
|---|---|
| `github.com/agent-fox-dev/agentkit-go/sandbox` | Sandboxes: the API, the `local`, `podman`, `landlock` and `gateway` providers, and the `agentkit-sandboxd` agent. |
| `github.com/agent-fox-dev/agentkit-go/mcpserve` | Serving a set of tools over MCP to another harness. |
| `github.com/agent-fox-dev/agentkit-go/codesearch` | An indexed `code_search` tool for large repositories. |

The sandbox gateway server (`sandbox/gatewayd`, the `agentkit-gateway`
command) is built from a checkout; see [`docs/cli.md`](docs/cli.md).

## Documentation

| Document | Read it to |
|---|---|
| [User guide](docs/guide.md) | Learn each part with examples: providers and credentials, tools and the guard, code mode, MCP, running tools without a model, sandboxes, the gateway, code search. **Start here.** |
| [Examples](examples/README.md) | Run ten small, self-contained programs, most of them offline. |
| [API map](docs/api.md) | Find the package and identifier for a task. |
| [Configuration reference](docs/configuration.md) | Look up every option, environment variable, default and limit. |
| [Command-line programs](docs/cli.md) | Run `agentkit-sandboxd` and `agentkit-gateway`. |
| [Architecture](docs/architecture.md) | Understand how a call and a run flow through the code, and the invariants behind it. |
| [Development](docs/development.md) | Build, test and contribute to AgentKit. |
| [Releasing](docs/releasing.md) | Publish the sandbox image. |
| [Dependencies](docs/DEPS.md) | See why each third-party module is allowed. |

The generated package reference (`go doc -all` for every public package) is in
[`docs/generated/`](docs/generated); `go doc` works as usual too.

## Development

```bash
git clone https://github.com/agent-fox-dev/agentkit-go
cd agentkit-go
make check     # format, vet, lint and the fast tests in every module; no API key needed
```

[`docs/development.md`](docs/development.md) covers the repository layout, the
make targets, the opt-in container and live-provider tests, and the
spec-driven workflow.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
