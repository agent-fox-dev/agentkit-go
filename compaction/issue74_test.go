package compaction

import (
	"context"
	"testing"

	"github.com/agentfox/agentkit-go/core"
)

// replying is a provider that answers every request with msg.
func replying(msg core.AssistantMessage) core.ProviderClient {
	return core.ClientFunc(func(context.Context, *core.Model, core.Request, core.ProviderStreamOptions) *core.EventStream {
		s := core.NewEventStream(core.StreamOptions{})
		m := msg
		s.End(core.StreamResult{Message: &m})
		return s
	})
}

// Issue #74 §1: the model summarizer reports what its request cost, through
// the context, whether or not the summary it got back is usable — a rejected
// summary was still billed.
func TestModelSummarizerReportsItsUsage(t *testing.T) {
	for _, stopReason := range []core.StopReason{core.StopReasonStop, core.StopReasonLength} {
		var u core.Usage
		u.SetField(core.UsageInputTokens, 600_000)
		u.SetCost(3)
		sum := ModelSummarizer(replying(core.AssistantMessage{
			Content: core.Content{core.TextBlock{Text: "a summary"}}, StopReason: stopReason, Usage: u,
		}), &core.Model{MaxTokens: 1000}, 0)

		var reported core.Usage
		ctx := core.WithUsageReporter(context.Background(), func(u core.Usage) { reported = reported.Add(u) })
		_, _ = sum(ctx, core.Messages{user("q")}, "")
		if reported.CostUSD != 3 || reported.InputTokens != 600_000 {
			t.Errorf("stop %q: reported %+v, want the request's $3 and 600K input tokens", stopReason, reported)
		}
	}
}

// Issue #74 §2: an aborted summarization is a truncated summary, and
// compaction is permanent — it must never become the checkpoint.
func TestAnAbortedSummaryNeverCheckpoints(t *testing.T) {
	sum := ModelSummarizer(replying(core.AssistantMessage{
		Content: core.Content{core.TextBlock{Text: "HALF A SUMM"}}, StopReason: core.StopReasonAborted,
	}), &core.Model{MaxTokens: 1000}, 0)

	h := core.NewConversationHistory()
	var persisted []core.CompactionCheckpoint
	var reported error
	msgs := longConversation(10)
	tf := NewContextTransform(Deps{
		Strategy:     Summarization{ThresholdFraction: 0.1, KeepTokens: 1000},
		Summarizer:   sum,
		History:      h,
		Model:        &core.Model{ContextWindow: 10000},
		OnCheckpoint: func(cp core.CompactionCheckpoint) error { persisted = append(persisted, cp); return nil },
		OnError:      func(err error) { reported = err },
	})
	out := tf(context.Background(), msgs)

	if cp, ok := h.Checkpoint(); ok {
		t.Fatalf("checkpoint set to %+v from an aborted summary", cp)
	}
	if len(persisted) != 0 {
		t.Fatalf("OnCheckpoint persisted %+v from an aborted summary", persisted)
	}
	if len(out) != len(msgs) {
		t.Fatal("an aborted summarization must leave the current view unchanged")
	}
	if reported == nil {
		t.Fatal("the aborted summarization must be surfaced through OnError")
	}
}

func assistantThinking(thought, text string) core.Message {
	return core.AssistantMessage{
		Content: core.Content{
			core.ThinkingBlock{Thinking: thought, Signature: "sig-" + thought},
			core.TextBlock{Text: text},
		},
		StopReason: core.StopReasonStop,
	}
}

func thinkingIn(m core.Message) []string {
	am, ok := m.(core.AssistantMessage)
	if !ok {
		return nil
	}
	var out []string
	for _, b := range am.Content {
		if tb, ok := b.(core.ThinkingBlock); ok {
			out = append(out, tb.Thinking)
		}
	}
	return out
}

// Issue #74 §3: a signed thinking block is bound to the prefix it was
// produced under. A tail message kept verbatim after the summary was
// produced under the full history, so its thinking is stripped from the
// view (its text and tool calls stay); thinking produced after the
// checkpoint, under the summary, is kept. The transcript is not touched.
func TestTheCompactedViewDropsThinkingProducedUnderTheOldPrefix(t *testing.T) {
	msgs := core.Messages{
		user("q0"), assistantThinking("t1", "a1"), // 0, 1: summarized
		user("q2"), assistantThinking("t3", "a3"), // 2, 3: kept, produced before the checkpoint
		user("q4"), assistantThinking("t5", "a5"), // 4, 5: produced after it
	}
	cp := core.CompactionCheckpoint{PrefixLen: 2, Summary: "S", CreatedAtLen: 4}
	view := ApplyCheckpoint(msgs, cp)

	if len(view) != 5 {
		t.Fatalf("view has %d messages, want summary + 4", len(view))
	}
	if got := thinkingIn(view[2]); len(got) != 0 {
		t.Errorf("view[2] (original 3) still carries thinking %v produced under the pre-summary prefix", got)
	}
	if got := view[2].(core.AssistantMessage).Content.Text(); got != "a3" {
		t.Errorf("view[2] text = %q; stripping thinking must keep the text", got)
	}
	if got := thinkingIn(view[4]); len(got) != 1 || got[0] != "t5" {
		t.Errorf("view[4] (original 5) thinking = %v; thinking produced under the summary must be kept", got)
	}
	if got := thinkingIn(msgs[3]); len(got) != 1 {
		t.Error("ApplyCheckpoint mutated the transcript: the original message lost its thinking")
	}

	// A checkpoint whose creation length is unknown (zero) strips nothing.
	view = ApplyCheckpoint(msgs, core.CompactionCheckpoint{PrefixLen: 2, Summary: "S"})
	if got := thinkingIn(view[2]); len(got) != 1 {
		t.Errorf("with CreatedAtLen unknown, thinking = %v; nothing can be said about its prefix", got)
	}
}

// The transform's own output after creating a checkpoint follows the same
// rule: every kept message predates the new checkpoint.
func TestANewCheckpointViewHasNoStaleThinking(t *testing.T) {
	var msgs core.Messages
	for i := 0; i < 10; i++ {
		msgs = append(msgs, longConversation(1)[0], assistantThinking("t", "answer"))
	}
	tf := NewContextTransform(Deps{
		Strategy:   Summarization{ThresholdFraction: 0.1, KeepTokens: 1000},
		Summarizer: func(context.Context, core.Messages, string) (string, error) { return "S", nil },
		History:    core.NewConversationHistory(),
		Model:      &core.Model{ContextWindow: 10000},
	})
	view := tf(context.Background(), msgs)
	if len(view) >= len(msgs) {
		t.Fatal("expected a compacted view")
	}
	for i, m := range view {
		if got := thinkingIn(m); len(got) != 0 {
			t.Errorf("view[%d] carries thinking produced before the summary: %v", i, got)
		}
	}
}

// Issue #84: Summarization cuts with CutNotToolResult, which may land on an
// assistant message — fine when a summary is prepended. With no Summarizer
// the prefix is just dropped, so the kept tail must start on a user message,
// as the window strategies guarantee; a view starting on an assistant turn is
// rejected by Anthropic and not repaired by REQ-PROV-11.
func TestWithNoSummarizerTheViewStartsOnAUserMessage(t *testing.T) {
	msgs := longConversation(10) // user, assistant, … ~1000 tokens each
	for keep := 500; keep <= 6000; keep += 500 {
		tf := NewContextTransform(Deps{
			Strategy: Summarization{ThresholdFraction: 0.5, KeepTokens: keep},
			History:  core.NewConversationHistory(),
			Model:    &core.Model{ContextWindow: 10000},
		})
		view := tf(context.Background(), msgs)
		if len(view) > 0 && view[0].Role() != core.RoleUser {
			t.Fatalf("KeepTokens %d: view of %d messages starts on %q, want a user message",
				keep, len(view), view[0].Role())
		}
	}
}

// Issue #84: a nil Deps.History is not a panic. The transform keeps its own
// checkpoint, so compaction is still permanent within it.
func TestANilHistoryIsNotAPanic(t *testing.T) {
	calls := 0
	tf := NewContextTransform(Deps{
		Strategy:   Summarization{ThresholdFraction: 0.1, KeepTokens: 1000},
		Summarizer: func(context.Context, core.Messages, string) (string, error) { calls++; return "S", nil },
		Model:      &core.Model{ContextWindow: 10000},
	})
	msgs := longConversation(10)
	for i := 0; i < 3; i++ {
		_ = tf(context.Background(), msgs)
	}
	if calls != 1 {
		t.Fatalf("summarized %d times over an unchanged history, want once: the checkpoint must persist", calls)
	}
}
