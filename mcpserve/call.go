package mcpserve

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// handler serves the tool called name. Every call goes to the runner and only
// there: the runner prepares the arguments, asks the guard, runs the tool with
// a nested caller where it reaches other tools, and calls the toolset's hooks,
// so a call from a harness and a nested call from a code_mode script pass the
// same guard. The handler adds no preparation, guard or validation, and never
// returns a Go error: a failure is a result the harness's model reads.
func (s *Server) handler(name string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		ctx, stop := endWithLifetime(ctx)
		defer stop()
		start := time.Now()
		res := s.run.Call(ctx, name, req.Params.Arguments)
		s.logCall(name, res, time.Since(start))
		return resultOf(res), nil
	}
}

// resultOf is what a harness reads of res: the text AgentKit's driver shows a
// model (core.ToolResult.LLMText), then each text block of the result in order.
// Other blocks, Data, Metadata and the Terminate vote are not served, and a
// failed call is an error result, not a protocol error.
func resultOf(res core.ToolResult) *sdk.CallToolResult {
	content := []sdk.Content{&sdk.TextContent{Text: res.LLMText()}}
	for _, b := range res.Blocks {
		if tb, ok := b.(core.TextBlock); ok {
			content = append(content, &sdk.TextContent{Text: tb.Text})
		}
	}
	return &sdk.CallToolResult{Content: content, IsError: !res.OK}
}

// logCall records one served call: the tool, whether it succeeded, how long it
// took and, for a failure, the runner's error code. It never carries the
// call's arguments, its result text or its Detail, which are workspace content.
func (s *Server) logCall(tool string, res core.ToolResult, d time.Duration) {
	attrs := []any{"tool", tool, "ok", res.OK, "duration", d}
	if !res.OK {
		attrs = append(attrs, "error", errorCode(res.Error))
	}
	s.log.Info("tool call", attrs...)
}

// errorCode is the runner's error code as it may be logged. The runner's codes
// are short identifiers (blocked_by_policy, handler_error, ...), but a tool
// chooses the Error of its own result and could put text there, which is
// workspace content; anything that is not a short identifier logs as "other".
func errorCode(code string) string {
	if code == "" || len(code) > 64 {
		return "other"
	}
	for _, c := range code {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "other"
		}
	}
	return code
}

// lifetimeKey carries a session's or an endpoint's lifetime in the context the
// SDK server was connected with. The SDK keeps the values of that context for
// every call it serves, and not its cancellation.
type lifetimeKey struct{}

// withLifetime returns ctx carrying life.
func withLifetime(ctx, life context.Context) context.Context {
	return context.WithValue(ctx, lifetimeKey{}, life)
}

// endWithLifetime returns ctx, cancelled also when the lifetime it carries
// ends, and a function that releases that tie.
func endWithLifetime(ctx context.Context) (context.Context, func()) {
	life, ok := ctx.Value(lifetimeKey{}).(context.Context)
	if !ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
