package ollama

import (
	"encoding/json"
	"fmt"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// chatTools declares the request's tools, in request order, as function tools. A
// tool choice of none declares none and sends no tools key at all, because Ollama
// has no tool choice; unset and auto send the tools.
func chatTools(req core.Request) (api.Tools, error) {
	if req.ToolChoice == core.ToolChoiceNone {
		return nil, nil
	}
	var out api.Tools
	for _, t := range req.Tools {
		params, err := toolParameters(t)
		if err != nil {
			return nil, fmt.Errorf("ollama: the input schema of tool %q is not one Ollama's tool type can carry: %w", t.Name, err)
		}
		out = append(out, api.Tool{
			Type:     "function",
			Function: api.ToolFunction{Name: t.Name, Description: t.Description, Parameters: params},
		})
	}
	return out, nil
}

// toolParameters is the part of a tool's input schema that Ollama's tool type
// carries. The schema is encoded as JSON and decoded into the SDK's parameter
// type, so whatever the type does not model is dropped (additionalProperties,
// format, bounds), and that grows with the SDK without code here. The type
// models an items schema and $defs as any, so what is inside them stays. A tool
// with no schema, or one without properties, is an empty object schema: the
// type's properties has no omitempty and would be sent as null.
func toolParameters(t core.ToolWire) (api.ToolFunctionParameters, error) {
	var p api.ToolFunctionParameters
	if t.InputSchema != nil {
		raw, err := t.InputSchema.MarshalJSON()
		if err != nil {
			return p, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, err
		}
	}
	if p.Type == "" {
		p.Type = "object"
	}
	if p.Properties == nil {
		p.Properties = api.NewToolPropertiesMap()
	}
	return p, nil
}
