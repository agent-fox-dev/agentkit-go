# AgentKit examples

Ten small programs, each showing one part of AgentKit. Every example is a
single `main.go` you can read top to bottom. They repeat their setup rather
than share a helper package, so everything you need is in the file you have
open. Each has a `README.md` saying what it shows and how to run it.

## Contents

- [The examples](#the-examples)
- [Where to start](#where-to-start)
- [Choosing a provider for the model-calling examples](#choosing-a-provider-for-the-model-calling-examples)

## The examples

Seven run **offline**, with no API key and no network: they use the scripted
model `provider/faux`, or no model at all. Three call a real model.

| Example | Shows | Needs | Run it |
|---|---|---|---|
| [`agentdemo`](agentdemo) | The agent loop and its event stream, step by step. | nothing | `go run ./examples/agentdemo` |
| [`toolbox`](toolbox) | Every built-in tool, called by hand through a `Runner`; try them on your own directory. | nothing | `go run ./examples/toolbox` |
| [`codemode`](codemode) | Code mode: one tool that runs a model-written script over other tools. | nothing | `go run ./examples/codemode` |
| [`sandbox`](sandbox) | A sandbox: syncing a project in, proxy tools, a guarded script, exporting results. | nothing | `cd examples/sandbox && go run .` |
| [`gateway`](gateway) | A sandbox gateway and a client that holds only its URL and token. | nothing | `cd examples/gateway && go run .` |
| [`harness`](harness) | Serving AgentKit's tools over MCP to another harness (Claude Code, Pi) working in a sandbox. | nothing offline; Podman and a harness for the real run | `cd examples/harness && go run .` |
| [`indexedsearch`](indexedsearch) | The `code_search` index: queries, symbol lookup, keeping it current. | nothing | `cd examples/indexedsearch && go run .` |
| [`codingagent`](codingagent) | A small coding agent: built-in tools, a workspace and the shell guard. | a model | `go run ./examples/codingagent --dir . "which files define the tool policy?"` |
| [`customtools`](customtools) | Writing your own tools well: schemas, argument repair, guidelines, ending a run. | a model | `go run ./examples/customtools` |
| [`mcp`](mcp) | Importing the tools of an MCP server, and the name-clash protection. | a model | `go run ./examples/mcp` |

The `sandbox`, `gateway`, `harness` and `indexedsearch` examples are their own
Go modules, so run them from their directory.

## Where to start

1. [`agentdemo`](agentdemo) to see what a run is.
2. [`toolbox`](toolbox) to see what the tools do.
3. [`codingagent`](codingagent) for a real agent on your own code.

Then pick the example for the feature you need. The
[user guide](../docs/guide.md) explains each feature in prose, with the same
building blocks.

## Choosing a provider for the model-calling examples

`codingagent`, `customtools` and `mcp` read three environment variables of
their own and pass them to `agentkit.Config`. The library itself never reads
them.

| Variable | Sets | Default |
|---|---|---|
| `AGENTKIT_PROVIDER` | `Config.Provider`: `anthropic`, `openrouter` or `ollama` | `anthropic` |
| `AGENTKIT_MODEL` | `Config.Model`: a tier (`SIMPLE`, `STANDARD`, `ADVANCED`) or a model id | `STANDARD` |
| `AGENTKIT_EFFORT` | `Config.Effort`: `low`, `medium`, `high`, `xhigh` or `max` | the tier's effort |

The provider then reads its own credential: `ANTHROPIC_API_KEY` (or Vertex AI
or Bedrock settings), `OPENROUTER_API_KEY`, or `OLLAMA_HOST` for an Ollama
server.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./examples/codingagent "summarise this repository"

AGENTKIT_PROVIDER=openrouter OPENROUTER_API_KEY=sk-or-... go run ./examples/customtools

ollama pull qwen3.8:27b
AGENTKIT_PROVIDER=ollama AGENTKIT_MODEL=SIMPLE go run ./examples/codingagent "hello"
```

Each prints a summary line with the provider, model and effort it ran, the
turns, tokens and cost. See [Choosing a provider](../docs/guide.md#choosing-a-provider)
for every credential and deployment, and
[Troubleshooting](../docs/guide.md#troubleshooting) when something fails.
