// Package codemode provides a tool that runs a model-written Starlark script
// whose functions are other tools. The model can chain calls, run them
// concurrently and filter their results inside one tool call, and only what
// the script prints and returns reaches the conversation.
//
// The script is sandboxed: it has no file system, network, environment,
// processes or clock, only the tools bound to it. Every call it makes goes
// through the agent's own nested-call pipeline (core.CallNested), so
// interceptors and events see each one as they see a call the
// model makes directly.
package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// outputSchema is the Data of every code-mode tool. It is one value shared by
// all of them, which is also how New recognizes a code-mode tool it is asked
// to bind, whatever that tool is called.
var outputSchema = schema.Object(
	schema.Prop("output", schema.String("What the script printed, and its return value")),
	schema.Prop("return_value", &schema.Schema{Description: "The script's return value, any JSON value"}),
	schema.Prop("calls_completed", schema.Array(schema.Object(
		schema.Prop("tool", schema.String()),
		schema.Prop("arguments", schema.Object()),
		schema.Prop("ok", schema.Bool()),
		schema.Opt("error", schema.String()),
	), "The nested tool calls the script made, in order")),
)

var defaultGuidelines = []string{
	"Use code_mode to chain several tool calls, run independent calls concurrently with parallel(...), " +
		"or filter a large result down before it reaches the conversation.",
	"Inside code_mode, tools take keyword arguments only and return an error value rather than raising; " +
		"check is_error(result) before using a result.",
}

// New returns a tool that runs a Starlark script over tools. The tool
// declares tools as its ReachableTools, so the agent's tool policy and shell
// guard look through it, and its description declares each tool as a typed
// function, generated from the tools' own schemas.
//
// New refuses a tool set that contains a code-mode tool (by opts.Name, or
// any tool New built, under any name and at any depth) and one with two
// tools of the same name.
func New(tools []core.Tool, opts Options) (core.Tool, BuildInfo, error) {
	opts = opts.withDefaults()
	seen := make(map[string]bool, len(tools))
	for _, t := range tools {
		if err := checkBindable(t.Name); err != nil && t.Name != opts.Name {
			return core.Tool{}, BuildInfo{}, err
		}
		if seen[t.Name] {
			return core.Tool{}, BuildInfo{}, fmt.Errorf("codemode: duplicate tool name: %s", t.Name)
		}
		seen[t.Name] = true
	}
	if t, found := findCodeMode(tools, opts.Name, nil); found {
		return core.Tool{}, BuildInfo{}, fmt.Errorf(
			"codemode: cannot bind code_mode tool inside code_mode (%q is a code-mode tool)", t.Name)
	}

	desc := opts.Description
	if desc == "" {
		var err error
		if desc, err = renderDescription(tools, opts); err != nil {
			return core.Tool{}, BuildInfo{}, err
		}
	}
	guidelines := opts.Guidelines
	if guidelines == nil {
		guidelines = append([]string(nil), defaultGuidelines...)
	}
	tool := core.Tool{
		Name:        opts.Name,
		Description: desc,
		InputSchema: schema.Object(
			schema.Prop("script", schema.String("The Starlark script to execute.")),
		),
		OutputSchema:     outputSchema,
		ReachableTools:   tools,
		PromptGuidelines: guidelines,
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return execute(ctx, opts, tools, in)
		},
	}
	return tool, BuildInfo{
		DescriptionBytes: len(tool.Description),
		DescriptionChars: utf8.RuneCountInString(tool.Description),
		BoundToolsCount:  len(tools),
	}, nil
}

// findCodeMode looks for a code-mode tool — named name, or built by New
// under any name — anywhere in tools' reachable hierarchy. It walks every
// path rather than core.ReachableTools' closure, which keeps one tool per
// name and could hide a code-mode tool behind a same-named plain one. path
// stops a cycle, which the agent refuses later anyway.
func findCodeMode(tools []core.Tool, name string, path []string) (core.Tool, bool) {
	for _, t := range tools {
		if t.Name == name || t.OutputSchema == outputSchema {
			return t, true
		}
		if slices.Contains(path, t.Name) {
			continue
		}
		if found, ok := findCodeMode(t.ReachableTools, name, append(path[:len(path):len(path)], t.Name)); ok {
			return found, true
		}
	}
	return core.Tool{}, false
}
