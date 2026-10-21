package provider_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

func call(id string) core.ToolUseBlock {
	return core.ToolUseBlock{ID: id, Name: "tool_" + id, Input: json.RawMessage(`{}`)}
}

func result(id string, isError bool) core.ToolResultMessage {
	return core.ToolResultMessage{ToolUseID: id, ToolName: "tool_" + id,
		Content: core.Content{core.TextBlock{Text: "result " + id}}, IsError: isError}
}

func say(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

// resultIDs returns the call ids of the tool results in ms, in order.
func resultIDs(ms core.Messages) []string {
	var ids []string
	for _, m := range ms {
		if tr, ok := m.(core.ToolResultMessage); ok {
			ids = append(ids, tr.ToolUseID)
		}
	}
	return ids
}

// repairFixture is a transcript with one of everything the repair acts on: an
// errored and an aborted assistant turn (the errored one with a result), and a
// kept turn that calls A, B and C and has a result only for B.
func repairFixture() core.Messages {
	return core.Messages{
		say("q1"),
		core.AssistantMessage{Content: core.Content{call("E")}, StopReason: core.StopReasonError, ErrorMessage: "boom"},
		result("E", false),
		core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "cut off"}}, StopReason: core.StopReasonAborted},
		say("q2"),
		core.AssistantMessage{
			Content: core.Content{
				core.ThinkingBlock{Thinking: "plan", Signature: `[{"type":"reasoning.text"}]`},
				call("A"), call("B"), call("C"),
			},
			StopReason: core.StopReasonToolUse,
		},
		result("B", false),
		say("q3"),
	}
}

// TS-27-18: the shared repair drops errored and aborted turns and the results
// that answer nothing, inserts a synthetic result for each unanswered call in
// that call's position, and leaves its input untouched.
func TestRepairToolPairing_TS27_18(t *testing.T) {
	in, snapshot := repairFixture(), repairFixture()
	out, rep := provider.RepairToolPairing(in)

	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("the input changed:\n got %#v\nwant %#v", in, snapshot)
	}
	if rep.DroppedAssistants != 2 || rep.DroppedResults != 1 || rep.Inserted != 2 || !rep.Changed() {
		t.Errorf("report = %+v, want 2 assistants and 1 result dropped and 2 results inserted", rep)
	}

	if got, want := resultIDs(out), []string{"A", "B", "C"}; !slices.Equal(got, want) {
		t.Fatalf("result ids = %v, want %v", got, want)
	}
	// q1, q2, the kept turn, A B C, q3.
	if len(out) != 7 {
		t.Fatalf("the output has %d messages, want 7: %#v", len(out), out)
	}
	for _, i := range []int{3, 5} {
		syn := out[i].(core.ToolResultMessage)
		if !syn.IsError || syn.Content.Text() != "No result provided" {
			t.Errorf("out[%d] = %+v, want an error result with the text \"No result provided\"", i, syn)
		}
	}
	if got := out[4].(core.ToolResultMessage); got.IsError || got.Content.Text() != "result B" {
		t.Errorf("out[4] = %+v, want B's own result", got)
	}
	if _, ok := out[6].(core.UserMessage); !ok {
		t.Errorf("out[6] is %T, want the user message the results come before", out[6])
	}
	// The kept turn is the same message, thinking and all.
	if !reflect.DeepEqual(out[2], in[5]) {
		t.Errorf("the kept assistant message changed: %#v", out[2])
	}
}

// A transcript that needs no repair comes back as it was, with an empty
// report.
func TestRepairToolPairingLeavesAGoodTranscriptAlone(t *testing.T) {
	good := core.Messages{
		say("q"),
		core.AssistantMessage{Content: core.Content{call("A"), call("B")}, StopReason: core.StopReasonToolUse},
		result("B", false), // answered out of call order: left as recorded
		result("A", true),
		core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	}
	out, rep := provider.RepairToolPairing(good)
	if rep.Changed() || rep.String() != "no repairs" {
		t.Errorf("report = %+v (%s), want no repairs", rep, rep)
	}
	if !reflect.DeepEqual(out, good) {
		t.Errorf("output = %#v, want the input as it was", out)
	}
	if out, rep := provider.RepairToolPairing(nil); len(out) != 0 || rep.Changed() {
		t.Errorf("an empty transcript gave %v, %+v", out, rep)
	}
}

// genTranscript builds a transcript from a seed: user turns and assistant turns
// of every stop reason, with thinking, text and tool calls, whose results are
// answered, left out, repeated, shuffled or made up.
func genTranscript(seed int64) core.Messages {
	r := rand.New(rand.NewSource(seed))
	var ms core.Messages
	next := 0
	for range 1 + r.Intn(8) {
		if r.Intn(4) == 0 {
			ms = append(ms, say(fmt.Sprintf("user %d", r.Intn(100))))
			continue
		}
		var content core.Content
		if r.Intn(2) == 0 {
			content = append(content, core.ThinkingBlock{Thinking: "t", Signature: fmt.Sprintf(`[{"index":%d}]`, r.Intn(5))})
		}
		if r.Intn(2) == 0 {
			content = append(content, core.TextBlock{Text: "text"})
		}
		var ids []string
		for range r.Intn(4) {
			next++
			id := fmt.Sprintf("c%d", next)
			ids = append(ids, id)
			content = append(content, call(id))
		}
		stop := []core.StopReason{core.StopReasonToolUse, core.StopReasonStop, core.StopReasonLength,
			core.StopReasonError, core.StopReasonAborted}[r.Intn(5)]
		ms = append(ms, core.AssistantMessage{Content: content, StopReason: stop})

		var results core.Messages
		for _, id := range ids {
			if r.Intn(10) < 6 {
				results = append(results, result(id, r.Intn(5) == 0))
				if r.Intn(8) == 0 {
					results = append(results, result(id, false)) // a repeated answer
				}
			}
		}
		if r.Intn(4) == 0 {
			results = append(results, result(fmt.Sprintf("orphan%d", r.Intn(50)), false))
		}
		r.Shuffle(len(results), func(i, j int) { results[i], results[j] = results[j], results[i] })
		ms = append(ms, results...)
	}
	return ms
}

// checkPaired fails the test unless every tool call of a kept assistant message
// is followed by exactly one result with its id before the next non-result
// message, and every result sits in the run of an assistant message that made
// the call.
func checkPaired(t *testing.T, seed int64, out core.Messages) {
	t.Helper()
	for i, m := range out {
		switch v := m.(type) {
		case core.AssistantMessage:
			if v.StopReason.ShortCircuits() {
				t.Errorf("seed %d: out[%d] is a %s assistant message", seed, i, v.StopReason)
			}
			var want []string
			for _, tu := range core.ExtractToolUse(&v) {
				if !slices.Contains(want, tu.ID) {
					want = append(want, tu.ID)
				}
			}
			var got []string
			for j := i + 1; j < len(out); j++ {
				tr, ok := out[j].(core.ToolResultMessage)
				if !ok {
					break
				}
				got = append(got, tr.ToolUseID)
			}
			if !slices.Equal(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				t.Errorf("seed %d: out[%d] calls %v but its results answer %v", seed, i, want, got)
			}
		case core.ToolResultMessage:
			j := i
			for j > 0 {
				if _, ok := out[j].(core.ToolResultMessage); !ok {
					break
				}
				j--
			}
			am, ok := out[j].(core.AssistantMessage)
			if !ok || !slices.ContainsFunc(core.ExtractToolUse(&am), func(tu core.ToolUseBlock) bool { return tu.ID == v.ToolUseID }) {
				t.Errorf("seed %d: out[%d] answers %q, which the message before its run did not call", seed, i, v.ToolUseID)
			}
		}
	}
}

// TS-27-19 (property): over generated transcripts the repair pairs every call
// exactly once, never alters a kept message (so no thinking block), does not
// change its input and is a fixed point.
func TestRepairToolPairingProperty_TS27_19(t *testing.T) {
	for seed := int64(0); seed < 500; seed++ {
		in, snapshot := genTranscript(seed), genTranscript(seed)
		out, _ := provider.RepairToolPairing(in)

		if !reflect.DeepEqual(in, snapshot) {
			t.Fatalf("seed %d: the input changed", seed)
		}
		checkPaired(t, seed, out)

		// Every kept assistant message is the input's, in order: thinking
		// blocks are not touched.
		var keptIn, keptOut []core.Message
		for _, m := range in {
			if am, ok := m.(core.AssistantMessage); ok && !am.StopReason.ShortCircuits() {
				keptIn = append(keptIn, m)
			}
		}
		for _, m := range out {
			if _, ok := m.(core.AssistantMessage); ok {
				keptOut = append(keptOut, m)
			}
		}
		if !reflect.DeepEqual(keptOut, keptIn) {
			t.Errorf("seed %d: the kept assistant messages changed:\n got %#v\nwant %#v", seed, keptOut, keptIn)
		}

		again, rep := provider.RepairToolPairing(out)
		if rep.Changed() || !reflect.DeepEqual(again, out) {
			t.Errorf("seed %d: repairing the output again reported %+v and changed it", seed, rep)
		}
	}
}
