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

func resultIDs(ms core.Messages) []string {
	var ids []string
	for _, m := range ms {
		if tr, ok := m.(core.ToolResultMessage); ok {
			ids = append(ids, tr.ToolUseID)
		}
	}
	return ids
}

// repairFixture has one of everything the repair acts on: an errored turn with
// a result, an aborted turn, and a kept turn that calls A, B and C and has a
// result only for B.
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

// The repair drops failed turns and the results that answer nothing, inserts a
// synthetic error result for each unanswered call in that call's position, and
// leaves its input and the kept turn's thinking untouched.
func TestRepairToolPairingDropsFailedTurnsAndAnswersEveryCall(t *testing.T) {
	in, snapshot := repairFixture(), repairFixture()
	out, rep := provider.RepairToolPairing(in)

	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("the input changed:\n got %#v\nwant %#v", in, snapshot)
	}
	if rep.DroppedAssistants != 2 || rep.DroppedResults != 1 || rep.Inserted != 2 || !rep.Changed() {
		t.Errorf("report = %+v, want 2 assistants and 1 result dropped and 2 results inserted", rep)
	}
	if got := rep.String(); got != "2 failed turns dropped, 1 orphaned results dropped, 2 synthetic results inserted" {
		t.Errorf("report text = %q", got)
	}
	if got, want := resultIDs(out), []string{"A", "B", "C"}; !slices.Equal(got, want) {
		t.Fatalf("result ids = %v, want %v", got, want)
	}
	if len(out) != 7 { // q1, q2, the kept turn, A B C, q3
		t.Fatalf("the output has %d messages, want 7: %#v", len(out), out)
	}
	for _, i := range []int{3, 5} {
		syn := out[i].(core.ToolResultMessage)
		if !syn.IsError || syn.Content.Text() != provider.SyntheticResultText {
			t.Errorf("out[%d] = %+v, want an error result with the text %q", i, syn, provider.SyntheticResultText)
		}
	}
	if got := out[4].(core.ToolResultMessage); got.IsError || got.Content.Text() != "result B" {
		t.Errorf("out[4] = %+v, want B's own result", got)
	}
	if !reflect.DeepEqual(out[2], in[5]) {
		t.Errorf("the kept assistant message changed: %#v", out[2])
	}
}

func TestRepairToolPairingDropsResultsThatAnswerNoCallOfTheirTurn(t *testing.T) {
	in := core.Messages{
		result("lead", false), // follows no assistant message
		say("q"),
		core.AssistantMessage{Content: core.Content{call("A")}, StopReason: core.StopReasonToolUse},
		result("A", false),
		result("A", true), // a second answer
		result("ghost", false),
	}
	out, rep := provider.RepairToolPairing(in)
	if got := resultIDs(out); !slices.Equal(got, []string{"A"}) || out[2].(core.ToolResultMessage).IsError {
		t.Errorf("results = %v, want A's first answer only", got)
	}
	if rep.DroppedResults != 3 || rep.Inserted != 0 {
		t.Errorf("report = %+v, want 3 results dropped", rep)
	}
}

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
// of every stop reason, whose results are answered, left out, repeated,
// shuffled or made up.
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
					results = append(results, result(id, false))
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

// checkPaired fails unless every call of a kept assistant message is answered
// exactly once in the run of results after it, and every result answers a call
// of the message before its run.
func checkPaired(t *testing.T, seed int64, out core.Messages) {
	t.Helper()
	for i, m := range out {
		v, ok := m.(core.AssistantMessage)
		if !ok {
			continue
		}
		if v.StopReason.ShortCircuits() {
			t.Errorf("seed %d: out[%d] is a %s assistant message", seed, i, v.StopReason)
		}
		var want, got []string
		for _, tu := range core.ExtractToolUse(&v) {
			want = append(want, tu.ID)
		}
		for j := i + 1; j < len(out); j++ {
			tr, ok := out[j].(core.ToolResultMessage)
			if !ok {
				break
			}
			got = append(got, tr.ToolUseID)
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("seed %d: out[%d] calls %v but its results answer %v", seed, i, want, got)
		}
	}
	for i, m := range out { // a run of results always follows an assistant message
		if _, ok := m.(core.ToolResultMessage); !ok {
			continue
		}
		if i == 0 {
			t.Errorf("seed %d: the transcript starts with a tool result", seed)
		} else if _, ok := out[i-1].(core.UserMessage); ok {
			t.Errorf("seed %d: out[%d] is a tool result after a user message", seed, i)
		}
	}
}

// Over generated transcripts the repair pairs every call exactly once, keeps
// every surviving assistant message as it was, never changes its input, and
// is a fixed point.
func TestRepairToolPairingHoldsOnGeneratedTranscripts(t *testing.T) {
	for seed := int64(0); seed < 500; seed++ {
		in, snapshot := genTranscript(seed), genTranscript(seed)
		out, _ := provider.RepairToolPairing(in)
		if !reflect.DeepEqual(in, snapshot) {
			t.Fatalf("seed %d: the input changed", seed)
		}
		checkPaired(t, seed, out)

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
			t.Errorf("seed %d: the kept assistant messages changed", seed)
		}
		if again, rep := provider.RepairToolPairing(out); rep.Changed() || !reflect.DeepEqual(again, out) {
			t.Errorf("seed %d: repairing the output again reported %+v", seed, rep)
		}
	}
}
