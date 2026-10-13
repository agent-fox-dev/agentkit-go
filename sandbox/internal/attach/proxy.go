package attach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/wire"
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

// call sends one tool call to the agent and maps its answer. It is safe for
// concurrent use and holds no lock across the RPC; the batch executor
// decides what runs in sequence.
//
//   - A host context that ends before or during the call gives the batch
//     executor's aborted result; the cancelled RPC kills the agent's process
//     group, and the sandbox stays live.
//   - An input over MaxMessageBytes is arguments_too_large, without an RPC.
//   - A transport failure or a malformed response makes the sandbox
//     unavailable for good: sandbox_unavailable, with no retry.
func (s *sb) call(ctx context.Context, name string, input []byte) core.ToolResult {
	if ctx.Err() != nil {
		return core.ErrResult("aborted", "Operation aborted")
	}
	if err := s.gate(); err != nil {
		return unavailableResult(err)
	}
	req := &sandboxpb.CallRequest{Name: name, Input: input}
	if n := proto.Size(req); n > sandbox.MaxMessageBytes {
		return core.ErrResult("arguments_too_large",
			fmt.Sprintf("the call encodes to %d bytes, over the sandbox message limit of %d", n, sandbox.MaxMessageBytes))
	}
	resp, err := s.client.Call(s.outgoing(ctx), req)
	if err != nil {
		if ctx.Err() != nil {
			return core.ErrResult("aborted", "Operation aborted")
		}
		return unavailableResult(s.classify(ctx, "Call", err))
	}
	res, err := decodeResult(resp)
	if err != nil {
		cause := "malformed response: " + err.Error()
		s.fail(cause)
		return core.ErrResult("sandbox_unavailable", cause)
	}
	return res
}

// decodeResult maps an agent's answer onto a ToolResult. Data and Metadata
// are untrusted: each passes wire.Guard, Data must be a JSON object and is
// decoded with UseNumber (so re-encoding gives the agent's bytes), and
// Metadata must decode into core.ToolMetadata. Terminate and Blocks are
// never carried.
func decodeResult(resp *sandboxpb.CallResponse) (core.ToolResult, error) {
	res := core.ToolResult{OK: resp.Ok, Error: resp.Error, Detail: resp.Detail, Text: resp.Text}
	if len(resp.Data) > 0 {
		if err := wire.Guard(resp.Data, wire.Limits{}); err != nil {
			return res, fmt.Errorf("data: %w", err)
		}
		if b := bytes.TrimLeft(resp.Data, " \t\r\n"); len(b) == 0 || b[0] != '{' {
			return res, errors.New("data: not a JSON object")
		}
		d := json.NewDecoder(bytes.NewReader(resp.Data))
		d.UseNumber()
		if err := d.Decode(&res.Data); err != nil {
			return res, fmt.Errorf("data: %w", err)
		}
	}
	if len(resp.Metadata) > 0 {
		if err := wire.Guard(resp.Metadata, wire.Limits{}); err != nil {
			return res, fmt.Errorf("metadata: %w", err)
		}
		res.Metadata = &core.ToolMetadata{}
		if err := json.Unmarshal(resp.Metadata, res.Metadata); err != nil {
			return res, fmt.Errorf("metadata: %w", err)
		}
	}
	return res, nil
}

// unavailableResult is the result of a call the sandbox cannot run.
func unavailableResult(err error) core.ToolResult {
	if errors.Is(err, sandbox.ErrClosed) {
		return core.ErrResult("sandbox_unavailable", "sandbox closed")
	}
	return core.ErrResult("sandbox_unavailable", err.Error())
}
