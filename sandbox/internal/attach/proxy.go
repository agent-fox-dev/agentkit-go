package attach

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// proxy copies h's definition and runs its calls in the agent. No Execute
// closure of the host's tools.All is kept.
func (s *sb) proxy(h core.Tool) core.Tool {
	name := h.Name
	return core.Tool{
		Name:             h.Name,
		Description:      h.Description,
		InputSchema:      h.InputSchema,
		OutputSchema:     h.OutputSchema,
		PromptGuidelines: h.PromptGuidelines,
		Builtin:          h.Builtin,
		ExecutionMode:    h.ExecutionMode,
		PrepareArguments: h.PrepareArguments,
		Label:            h.Label,
		Execute: func(ctx context.Context, input json.RawMessage) core.ToolResult {
			return s.call(ctx, name, input)
		},
	}
}

// call sends one tool call to the agent and maps its answer.
func (s *sb) call(ctx context.Context, name string, input []byte) core.ToolResult {
	if err := s.gate(); err != nil {
		return unavailableResult(err)
	}
	resp, err := s.client.Call(s.outgoing(ctx), &sandboxpb.CallRequest{Name: name, Input: input})
	if err != nil {
		return unavailableResult(s.classify(ctx, "Call", err))
	}
	res := core.ToolResult{OK: resp.Ok, Error: resp.Error, Detail: resp.Detail, Text: resp.Text}
	if len(resp.Data) > 0 {
		d := json.NewDecoder(bytes.NewReader(resp.Data))
		d.UseNumber()
		if err := d.Decode(&res.Data); err != nil {
			s.fail("malformed response: data: " + err.Error())
			return core.ErrResult("sandbox_unavailable", "malformed response: data: "+err.Error())
		}
	}
	if len(resp.Metadata) > 0 {
		res.Metadata = &core.ToolMetadata{}
		if err := json.Unmarshal(resp.Metadata, res.Metadata); err != nil {
			s.fail("malformed response: metadata: " + err.Error())
			return core.ErrResult("sandbox_unavailable", "malformed response: metadata: "+err.Error())
		}
	}
	return res
}

// unavailableResult is the result of a call on a sandbox that cannot run
// it.
func unavailableResult(err error) core.ToolResult {
	if err == sandbox.ErrClosed {
		return core.ErrResult("sandbox_unavailable", "sandbox closed")
	}
	return core.ErrResult("sandbox_unavailable", err.Error())
}
