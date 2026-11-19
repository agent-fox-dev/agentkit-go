package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// definition is one served tool as `tools` lists it, and what a relay answers
// tools/list from.
type definition struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
}

// definitions lists ts in order. A tool with no input schema is listed with
// {"type":"object"}, as an MCP session serves it; one with no output schema
// with null.
func definitions(ts []core.Tool) ([]definition, error) {
	out := make([]definition, 0, len(ts))
	for _, t := range ts {
		d := definition{Name: t.Name, Description: t.Description,
			InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`null`)}
		if t.InputSchema != nil {
			b, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("tool %q: input schema: %w", t.Name, err)
			}
			d.InputSchema = b
		}
		if t.OutputSchema != nil {
			b, err := json.Marshal(t.OutputSchema)
			if err != nil {
				return nil, fmt.Errorf("tool %q: output schema: %w", t.Name, err)
			}
			d.OutputSchema = b
		}
		out = append(out, d)
	}
	return out, nil
}

// runTools prints the served tool definitions.
func runTools(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	var s settings
	s.register(fs)
	if parse(fs, args, stderr) != nil {
		return 2
	}
	if err := s.checkDir(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	srv, err := open(s, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	defer srv.close()
	defs, err := definitions(srv.runner.Tools())
	if err != nil {
		fmt.Fprintf(stderr, "agentkit-tools tools: %v\n", err)
		return 1
	}
	b, err := json.Marshal(defs)
	if err != nil {
		fmt.Fprintf(stderr, "agentkit-tools tools: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", b)
	return 0
}
