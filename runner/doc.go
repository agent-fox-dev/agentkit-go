// Package runner runs tool calls with no model. New turns a Toolset — the
// tools, the policy that selects among them, the guard, the after-interceptor
// and the event sink — into a Runner, whose Call and Batch run calls through
// the pipeline an agent run uses: argument preparation, the guard, the handler
// with a nested caller attached when the tool reaches other tools, the
// after-interceptor and the execution events. Code mode runs through it like
// any other tool.
//
// It is where a tools-only program starts: one that serves or calls tools and
// never calls a model. The package imports core and guard and nothing else of
// AgentKit, so such a program links neither a model provider's SDK nor the
// source tools' tree-sitter grammars. Package agentkit builds its Agent on a
// Runner and keeps Runner, Toolset and NewRunner as names for what is here
// (docs/adr/10-keep-the-runner-and-the-output-buffer-free-of-cgo-and-providers.md).
package runner
