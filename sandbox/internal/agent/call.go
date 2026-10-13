package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Call runs one tool call with the RPC context as the tool's context, so
// cancelling the RPC cancels the tool. A tool the set lacks, a result that
// does not marshal and a panic are failed results, not RPC errors.
func (a *Agent) Call(ctx context.Context, req *sandboxpb.CallRequest) (*sandboxpb.CallResponse, error) {
	a.mu.RLock()
	set := a.set
	a.mu.RUnlock()
	var t core.Tool
	ok := false
	if set != nil {
		t, ok = set.byName[req.Name]
	}
	if !ok {
		var names []string
		if set != nil {
			names = set.names
		}
		return &sandboxpb.CallResponse{Error: "unknown_tool",
			Detail: fmt.Sprintf("tool %q is not available; available: %s", req.Name, strings.Join(names, ", "))}, nil
	}
	return encodeResult(execute(ctx, t, req.Input)), nil
}

// execute runs t, turning a panic into a result as the host's
// invokeHandler does.
func execute(ctx context.Context, t core.Tool, input []byte) (res core.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			res = core.ErrResult("panic", fmt.Sprintf("tool %q panicked: %v", t.Name, r))
		}
	}()
	return t.Execute(ctx, json.RawMessage(input))
}

// encodeResult marshals res; a nil or empty Data or Metadata is sent as
// empty bytes.
func encodeResult(res core.ToolResult) *sandboxpb.CallResponse {
	out := &sandboxpb.CallResponse{Ok: res.OK, Error: res.Error, Detail: res.Detail, Text: res.Text}
	var err error
	if len(res.Data) > 0 {
		if out.Data, err = json.Marshal(res.Data); err != nil {
			return &sandboxpb.CallResponse{Error: "marshal_failed", Detail: "data: " + err.Error()}
		}
	}
	if !res.Metadata.IsEmpty() {
		if out.Metadata, err = json.Marshal(res.Metadata); err != nil {
			return &sandboxpb.CallResponse{Error: "marshal_failed", Detail: "metadata: " + err.Error()}
		}
	}
	return out
}
