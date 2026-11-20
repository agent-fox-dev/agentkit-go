package agentkit

import (
	"context"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testkit"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// testModel is a model id the catalog does not list: an agent on it runs on
// the fallback row of the default provider.
const testModel = "test-model"

// streamFunc is a provider client written as one function.
type streamFunc func(ctx context.Context, req core.Request) *core.EventStream

func (f streamFunc) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	return core.StreamChannel(f(ctx, req)), nil
}

// answering is a provider that ends every stream with msg and err.
func answering(msg *core.AssistantMessage, err error) streamFunc {
	return func(context.Context, core.Request) *core.EventStream {
		s := core.NewEventStream(core.StreamOptions{})
		s.End(core.StreamResult{Message: msg, Err: err})
		return s
	}
}

// mustNew builds an agent on cfg, on testModel when cfg names no model.
func mustNew(t *testing.T, cfg Config) *Agent {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = testModel
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// scripted builds an agent that replays turns over ts.
func scripted(t *testing.T, ts Toolset, turns ...faux.Turn) (*Agent, *faux.Provider) {
	t.Helper()
	fp := faux.New(turns...)
	return mustNew(t, Config{Client: fp, Toolset: ts}), fp
}

func mustRunner(t *testing.T, ts Toolset) *Runner {
	t.Helper()
	r, err := NewRunner(ts)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// calls is a turn that calls the given tools.
func calls(blocks ...core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: blocks, StopReason: core.StopReasonToolUse}
}

var call = faux.FauxToolCall

func use(t *testing.T, id, name, args string) core.ToolUseBlock {
	return testkit.ToolUse(t, id, name, args)
}

// drain streams one run of a and returns its events and its result.
func drain(t *testing.T, ctx context.Context, a *Agent, prompt string) ([]core.Event, core.RunResult, error) {
	t.Helper()
	s, err := a.Stream(ctx, prompt)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var evs []core.Event
	for e := range s.Events() {
		evs = append(evs, e)
	}
	res, err := s.RunResult()
	return evs, res, err
}

// resultOf is the tool result answering id, and whether there is one.
func resultOf(msgs core.Messages, id string) (core.ToolResultMessage, bool) {
	for _, m := range msgs {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolUseID == id {
			return r, true
		}
	}
	return core.ToolResultMessage{}, false
}

// The tool-side doubles are internal/testkit's, shared with the runner
// package's tests.
var (
	executions = testkit.Executions
	echo       = testkit.EchoTool
	tool       = testkit.Tool
	returning  = testkit.Returning
	wrapper    = testkit.Wrapper
	noop       = testkit.Noop
	named      = testkit.Named
	blockIf    = testkit.BlockIf
	nested     = testkit.Uses
	child      = testkit.Child
)

type (
	eventLog = testkit.EventLog
	guardLog = testkit.GuardLog
)
