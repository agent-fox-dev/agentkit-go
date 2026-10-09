package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// These benchmarks are REPORTED, not budgeted. Wall-clock thresholds failed
// under machine load (issue 92), so spec 09 removed the threshold tests that
// once paired with them. A budget that has to hold is pinned as a count or a
// structure instead: perf_wiring_test.go asserts that a steady-state request
// serializes zero schemas.

// benchAgent builds an agent whose provider returns instantly, so what is
// measured is the LOOP and nothing else (NFR-PERF-01 excludes model latency
// and tool execution time by definition).
func benchAgent(b *testing.B, tools int) *Agent {
	b.Helper()
	s := &scripted{}
	cfg := Config{Provider: streamFunc(s.stream), Model: testModelID}
	for i := 0; i < tools; i++ {
		cfg.Tools = append(cfg.Tools, echoTool(fmt.Sprintf("tool_%d", i), nil))
	}
	a, err := New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	return a
}

// BenchmarkLoopTurnOverhead is NFR-PERF-01: loop overhead per turn, excluding
// model API latency and tool execution.
//
// A FRESH agent per iteration, with the clock stopped around construction.
// Reusing one agent across b.N measures something else entirely — the
// transcript grows by a turn each iteration, so the reported ns/op is the
// average over a history that ends up thousands of turns deep. That is a real
// property, and it is BenchmarkLoopTurnAtDepth's job, not this one's. Averaging
// the two together produces a number that answers neither question and fails
// the budget for the wrong reason.
func BenchmarkLoopTurnOverhead(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := benchAgent(b, 8)
		b.StartTimer()
		if _, err := a.Run(ctx, "go"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoopTurnAtDepth measures one turn against an already-long
// transcript, which is where the loop's per-turn cost actually lives.
//
// NFR-PERF-01 budgets "per turn" without saying at what depth, and the
// difference is large: the REQ-GO-15 estimate and the send-time repair pass are
// both O(history), so a turn 500 messages deep costs meaningfully more than the
// first one. This benchmark is REPORTED rather than budgeted, because inventing
// a threshold the requirement does not state would be exactly the unenforceable
// rigour NFR-PERF-09 objects to.
func BenchmarkLoopTurnAtDepth(b *testing.B) {
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := benchAgentAtDepth(b, 500)
		b.StartTimer()
		if _, err := a.Run(ctx, "go"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLoopTurnWithToolBatch measures a turn that also dispatches a
// parallel tool batch, so the batch executor's own overhead is visible
// separately from the bare turn. A fresh agent per iteration, for the same
// reason as above.
func BenchmarkLoopTurnWithToolBatch(b *testing.B) {
	build := func() *Agent {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				mustUse("c1", "tool_0", `{"v":"x"}`),
				mustUse("c2", "tool_1", `{"v":"y"}`),
				mustUse("c3", "tool_2", `{"v":"z"}`)),
		}}
		cfg := Config{Provider: streamFunc(s.stream), Model: testModelID}
		for i := 0; i < 3; i++ {
			cfg.Tools = append(cfg.Tools, echoTool(fmt.Sprintf("tool_%d", i), nil))
		}
		a, err := New(cfg)
		if err != nil {
			b.Fatal(err)
		}
		return a
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := build()
		b.StartTimer()
		if _, err := a.Run(ctx, "go"); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------- NFR-PERF-07

// stampFixture is NFR-PERF-07's stated worst case: 128 tools and 1000
// messages.
func stampFixture(b testingTB) (*core.Model, core.Request) {
	m := &core.Model{ID: "claude-x", MaxOutputTokens: 4096}
	req := core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: "a system prompt"}},
	}
	for i := 0; i < 128; i++ {
		req.Tools = append(req.Tools, core.ToolWire{
			Name: fmt.Sprintf("tool_%d", i), Description: "a tool",
			InputSchema: schema.Object(schema.Opt("v", schema.String("v")))})
	}
	for i := 0; i < 500; i++ {
		req.Messages = append(req.Messages,
			core.UserMessage{Content: core.Content{core.TextBlock{Text: fmt.Sprintf("question %d", i)}}},
			core.AssistantMessage{
				Content:    core.Content{core.TextBlock{Text: fmt.Sprintf("answer %d", i)}},
				Model:      "claude-x",
				StopReason: core.StopReasonStop})
	}
	_ = b
	return m, req
}

type testingTB interface{ Helper() }

// BenchmarkCacheControlStamping is NFR-PERF-07's actual budget: "stamping the
// three markers must add less than 1 ms for tool sets up to 128 tools and
// transcripts up to 1000 messages".
//
// It measures the STAMP, on an already-built body, because that is what the
// number is about. The whole-request benchmarks below give the context the
// number needs — see the note there.
func BenchmarkCacheControlStamping(b *testing.B) {
	m, req := stampFixture(b)
	body, _, _, err := anthropic.BuildRequestCached(m, req, core.CacheRetentionNone, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		anthropic.StampCacheControl(body, core.CacheRetentionShort, m)
	}
}

// BenchmarkAnthropicBuildRequestUncached and its cached twin are what found
// the NFR-PERF-03 gap.
//
// The uncached build of a 128-tool, 1000-message request costs ~1.5 ms, and
// ~0.9 ms of that is re-serializing tool schemas that did not change — paid on
// every turn, for identical bytes. NFR-PERF-03 says that serialization "must
// be computed once per session and cached", and REQ-CACHE-06 was implemented
// and attached to nothing until this benchmark measured it.
func BenchmarkAnthropicBuildRequestUncached(b *testing.B) {
	m, req := stampFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := anthropic.BuildRequestCached(m, req, core.CacheRetentionShort, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnthropicBuildRequestCached(b *testing.B) {
	m, req := stampFixture(b)
	prefix := &provider.ToolPrefix{}
	if _, _, _, err := anthropic.BuildRequestCached(m, req, core.CacheRetentionShort, prefix); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := anthropic.BuildRequestCached(m, req, core.CacheRetentionShort, prefix); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------- NFR-PERF-03

func prefixTools(n int) []core.ToolWire {
	out := make([]core.ToolWire, n)
	for i := range out {
		out[i] = core.ToolWire{
			Name: fmt.Sprintf("tool_%d", i), Description: "a tool",
			InputSchema: schema.Object(
				schema.Opt("path", schema.String("path")),
				schema.Opt("limit", schema.Int("limit")),
				schema.Opt("mode", schema.Enum("mode", "a", "b", "c")))}
	}
	return out
}

// BenchmarkToolSchemaSerializationUncached is NFR-PERF-03's baseline: what a
// model call costs when the schemas are re-serialized every time.
func BenchmarkToolSchemaSerializationUncached(b *testing.B) {
	tools := prefixTools(128)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, t := range tools {
			if _, err := json.Marshal(t.InputSchema); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkToolSchemaSerializationCached is REQ-CACHE-06's steady state: the
// same reconciliation with nothing changed. NFR-PERF-03 says serialization
// must be computed ONCE PER SESSION, so the steady-state cost is the number
// that matters.
func BenchmarkToolSchemaSerializationCached(b *testing.B) {
	tools := prefixTools(128)
	var p provider.ToolPrefix
	if _, _, err := p.Sync(tools); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := p.Sync(tools); err != nil {
			b.Fatal(err)
		}
	}
}

func mustUse(id, name, args string) core.ToolUseBlock {
	b, err := core.NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		panic(err)
	}
	return b
}

// deepHistory builds a transcript of n user/assistant pairs.
func deepHistory(n int) core.Messages {
	var msgs core.Messages
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			core.UserMessage{Content: core.Content{core.TextBlock{
				Text: fmt.Sprintf("question number %d, with enough text to be realistic", i)}}},
			core.AssistantMessage{
				Content: core.Content{core.TextBlock{
					Text: fmt.Sprintf("answer number %d, likewise of a realistic length", i)}},
				Model:      "test-model",
				StopReason: core.StopReasonStop})
	}
	return msgs
}

func benchAgentAtDepth(b *testing.B, turns int) *Agent {
	b.Helper()
	s := &scripted{}
	a, err := New(Config{Provider: streamFunc(s.stream), Model: testModelID})
	if err != nil {
		b.Fatal(err)
	}
	a.transcript = deepHistory(turns)
	return a
}
