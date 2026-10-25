package mcpserve

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestACallReachesTheRunnerWithTheRequestsArgumentBytes(t *testing.T) {
	s, rec := serverOf(t, Options{})
	c := serveStdio(t, context.Background(), s)
	const odd = `{"text": "a",  "extra":[true]}`
	c.send(initFrame, readyFrame, call("1", `{"name":"echo","arguments":`+odd+`}`), call("2", `{"name":"echo"}`))
	testutil.Eventually(t, "both calls", func() bool { return len(rec.results()) == 2 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if inputs := slices.Sorted(slices.Values(rec.inputs)); !slices.Equal(inputs, []string{"", odd}) {
		t.Errorf("the runner received %q, want the request's own bytes and nothing for no arguments", inputs)
	}
}

func TestAResultIsServedAsItsLLMTextThenItsTextBlocks(t *testing.T) {
	blocks := []core.ContentBlock{core.TextBlock{Text: "one"}, core.ThinkingBlock{Thinking: "hidden", Signature: "s"},
		core.ToolUseBlock{ID: "id", Name: "n", Input: json.RawMessage(`{}`)}, core.TextBlock{Text: "two"}}
	for _, res := range []core.ToolResult{
		{OK: true, Text: "fine", Data: map[string]any{"k": 1}, Metadata: &core.ToolMetadata{Truncated: true}, Blocks: blocks, Terminate: true},
		{OK: false, Error: "handler_error", Detail: "it broke", Blocks: blocks},
	} {
		got, texts := resultOf(res), []string{}
		for _, c := range got.Content {
			texts = append(texts, c.(*sdk.TextContent).Text)
		}
		if want := []string{res.LLMText(), "one", "two"}; !slices.Equal(texts, want) || got.IsError == res.OK || got.StructuredContent != nil {
			t.Errorf("%+v is served as %q (isError %v, structured %v), want %q", res, texts, got.IsError, got.StructuredContent, want)
		}
	}
}

// A tool the server does not serve is a protocol error and never reaches the runner.
func TestRunnerFailuresAreErrorResultsAndAnUnservedToolAProtocolError(t *testing.T) {
	needsPath := tool("needs_path", nil)
	needsPath.InputSchema = schema.Object(schema.Prop("path", schema.String()))
	rec := &recorder{toolRunner: must(agentkit.NewRunner(agentkit.Toolset{
		Tools: []core.Tool{needsPath, tool("blocked", nil),
			tool("panics", func(context.Context, json.RawMessage) core.ToolResult { panic("boom") }),
			{Name: "fails", Description: "x", InputSchema: schema.Object(),
				Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("it broke") }}},
		Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: in.ToolName == "blocked", Reason: "not that one"}
		},
	}))}
	conn := serveStdio(t, context.Background(), must(newServer(rec, Options{}))).conn(t)
	for tool, want := range map[string]string{"needs_path": "invalid_arguments", "panics": "panic", "fails": "it broke", "blocked": "not that one"} {
		out, err := conn.CallRaw(context.Background(), tool, json.RawMessage(`{}`))
		if err != nil || !out.IsError || !strings.Contains(out.Content[0].(*agentmcp.TextContent).Text, want) {
			t.Errorf("%s: %+v, %v; want an error result naming %q", tool, out, err, want)
		}
	}
	_, err := conn.CallRaw(context.Background(), "nope", json.RawMessage(`{}`))
	if rpc := (*jsonrpc.Error)(nil); !errors.As(err, &rpc) || len(rec.results()) != 4 {
		t.Errorf("an unserved tool: %v (%T) after %d runner calls, want a JSON-RPC error and 4", err, err, len(rec.results()))
	}
}

// While one call is held a second completes, so calls are not serialized;
// ending ServeStdio's context ends the session, and ServeStdio returns its error.
func TestCallsRunConcurrentlyAndEndingTheRequestOrSessionCancelsThem(t *testing.T) {
	for _, endSession := range []bool{false, true} {
		started, observed := make(chan struct{}, 1), make(chan error, 1)
		s, rec := serverOf(t, Options{}, blockingTool(started, observed), echoTool())
		ctx, end := context.WithCancel(context.Background())
		c := serveStdio(t, ctx, s)
		conn := c.conn(t)
		callCtx, cancel := context.WithCancel(context.Background())
		go func() { _, _ = conn.CallRaw(callCtx, "block", nil) }()
		<-started
		if out, err := conn.CallRaw(context.Background(), "echo", nil); err != nil || out.IsError {
			t.Errorf("a call alongside a held one: %+v, %v", out, err)
		}
		if endSession {
			end()
			if err := <-c.done; err != context.Canceled {
				t.Errorf("ServeStdio returned %v, want context.Canceled", err)
			}
		} else {
			cancel()
		}
		testutil.Eventually(t, "the runner's result", func() bool { return len(rec.results()) == 2 })
		if got := rec.results()[1].Error; <-observed == nil || got != "aborted" {
			t.Errorf("session ended %v: the runner answered %q, want its context ended and aborted", endSession, got)
		}
		cancel()
		end()
	}
}

func TestOnlyAShortIdentifierIsLoggedAsAnErrorCode(t *testing.T) {
	for code, want := range map[string]string{"blocked_by_policy": "blocked_by_policy", "e2": "e2", "": "other",
		"Has Spaces": "other", "secret: text": "other", strings.Repeat("a", 65): "other"} {
		if got := errorCode(code); got != want {
			t.Errorf("errorCode(%q) = %q, want %q", code, got, want)
		}
	}
}
