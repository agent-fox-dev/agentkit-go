package codemode

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// DefaultDescriptionTemplate is the instructions section of a code-mode
// tool's description: the runtime's rules, which a model that knows Python
// but not Starlark needs spelled out. Options.DescriptionTemplate replaces
// it. The bound tools' declarations always follow it.
//
// The template's data is DescriptionData.
const DefaultDescriptionTemplate = `Run a Starlark script that calls tools as functions. Use it to chain tool calls, run independent calls concurrently, and filter large results down to what you need: only what the script prints and returns comes back to you.

Starlark is a Python 3 dialect without exceptions, classes or imports. Write functions with def; top-level for, if and while are allowed.

Rules:
- The script has no file system, network, environment, processes or clock. It can reach the outside world only through the {{.ToolCount}} tools declared below. load(...) is refused.
- Call a tool with keyword arguments only, matching its declaration: read_file(path="a.txt"). Positional arguments are an error.
- A call returns the tool's result (a dict) on success. On failure it returns a ToolError instead of raising: check is_error(r), r.ok or r.is_error, then read r.error (the code) and r.detail. A call an interceptor blocked is a ToolError too.
- Run independent calls concurrently with parallel([call(tool, arg=value), ...]); results come back as a list in the order given, at most {{.MaxConcurrentCalls}} in flight at a time.
- Output: print(...) what you want to read, and return a value from def main() or assign it to a global named result.
- Side effects are not undone: a tool call that completed stays done even if the script fails later.
- Limits: {{.MaxCalls}} tool calls, {{.MaxSteps}} execution steps, {{.MaxTimeout}} wall time and {{.MaxOutputBytes}} bytes of output per script. Hitting one ends the script with an error that names it, the partial output and the calls that completed.`

// DescriptionData is what a description template can use.
type DescriptionData struct {
	// Name is the code-mode tool's name.
	Name string
	// ToolCount is the number of bound tools; ToolNames are their names.
	ToolCount int
	ToolNames []string
	// The limits a script runs under.
	MaxTimeout         string
	MaxSteps           uint64
	MaxCalls           int
	MaxConcurrentCalls int
	MaxOutputBytes     int
}

// renderDescription is the instructions section followed by a declaration
// for every bound tool.
func renderDescription(tools []core.Tool, opts Options) (string, error) {
	src := opts.DescriptionTemplate
	if src == "" {
		src = DefaultDescriptionTemplate
	}
	tmpl, err := template.New(opts.Name).Option("missingkey=error").Parse(src)
	if err != nil {
		return "", fmt.Errorf("codemode: description template: %w", err)
	}
	data := DescriptionData{Name: opts.Name, ToolCount: len(tools), MaxTimeout: opts.MaxTimeout.String(),
		MaxSteps: opts.MaxSteps, MaxCalls: opts.MaxCalls, MaxConcurrentCalls: opts.MaxConcurrentCalls,
		MaxOutputBytes: opts.MaxOutputBytes}
	for _, t := range tools {
		data.ToolNames = append(data.ToolNames, t.Name)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("codemode: description template: %w", err)
	}
	b.WriteString("\n\nTools:\n")
	for _, t := range tools {
		b.WriteString("\n" + RenderSignature(t) + "\n")
	}
	return b.String(), nil
}
