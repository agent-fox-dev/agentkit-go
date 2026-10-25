package anthropic_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// self is the target the transcripts below are repaired for; its own turns
// are reply's, by claude-test.
var self = anthropic.Target{Model: "claude-test", NormalizeToolCallID: anthropic.NormalizeToolCallID}

func roles(ms core.Messages) string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Role()))
	}
	return strings.Join(out, ",")
}

func resultIDs(ms core.Messages) string {
	var out []string
	for _, m := range ms {
		if tr, ok := m.(core.ToolResultMessage); ok {
			out = append(out, tr.ToolUseID)
		}
	}
	return strings.Join(out, ",")
}

// checkSendable fails unless every tool_use in out is answered and every
// result answers a tool_use: the shape every provider accepts.
func checkSendable(t *testing.T, in, out core.Messages) {
	t.Helper()
	live, answered := map[string]bool{}, map[string]bool{}
	for _, m := range out {
		if am, ok := m.(core.AssistantMessage); ok {
			for _, tu := range core.ExtractToolUse(&am) {
				live[tu.ID] = true
			}
		}
	}
	for _, m := range out {
		if tr, ok := m.(core.ToolResultMessage); ok {
			if !live[tr.ToolUseID] {
				t.Errorf("orphaned result %q survived the repair of %s", tr.ToolUseID, roles(in))
			}
			answered[tr.ToolUseID] = true
		}
	}
	for id := range live {
		if !answered[id] {
			t.Errorf("tool_use %q unanswered after the repair of %s", id, roles(in))
		}
	}
}

// A failed or aborted turn is dropped, and so is every result it leaves
// without a call: the commonest damaged transcript is Ctrl-C during a tool
// batch, then resume.
func TestRepairDropsFailedTurnsAndTheResultsTheyOrphan(t *testing.T) {
	aborted := reply(toolUse("c1", "read", `{}`))
	aborted.StopReason = core.StopReasonAborted
	errored := reply(core.TextBlock{Text: "half"})
	errored.StopReason = core.StopReasonError
	in := core.Messages{user("hi"), aborted, result("c1", "contents"), errored, result("ghost", "x"), user("again")}
	out, rep := anthropic.RepairTranscript(in, self)
	if roles(out) != "user,user" || rep.DroppedFailedTurns != 2 || rep.DroppedOrphanResults != 2 {
		t.Fatalf("roles %s, report %+v", roles(out), rep)
	}
	checkSendable(t, in, out)
}

// A call nothing answered gets a synthetic error result, in its call's
// position among the turn's results, since some wires pair by position; a
// complete turn keeps the order its results were recorded in.
func TestRepairAnswersEveryCallInItsPosition(t *testing.T) {
	calls := reply(toolUse("c1", "read", `{}`), toolUse("c2", "read", `{}`), toolUse("c3", "read", `{}`))
	cases := []struct {
		name       string
		in         core.Messages
		ids, roles string
		synthetic  int
	}{
		{"a middle call unanswered", core.Messages{user("hi"), calls, result("c3", "r3"), result("c1", "r1"), user("next")},
			"c1,c2,c3", "user,assistant,tool_result,tool_result,tool_result,user", 1},
		{"a complete turn out of order", core.Messages{user("hi"), calls, result("c3", "r3"), result("c2", "r2"), result("c1", "r1")},
			"c3,c2,c1", "user,assistant,tool_result,tool_result,tool_result", 0},
		{"a call answered later in the transcript", core.Messages{user("hi"), calls, result("c1", "r1"), user("wait"),
			result("c2", "r2"), result("c3", "r3")}, "c1,c2,c3", "user,assistant,tool_result,user,tool_result,tool_result", 0},
		{"a turn cut off at the end", core.Messages{user("hi"), calls}, "c1,c2,c3", "user,assistant,tool_result,tool_result,tool_result", 3},
	}
	for _, c := range cases {
		out, rep := anthropic.RepairTranscript(c.in, self)
		if resultIDs(out) != c.ids || roles(out) != c.roles || rep.SyntheticResults != c.synthetic {
			t.Errorf("%s: results %s, roles %s, %d synthetic", c.name, resultIDs(out), roles(out), rep.SyntheticResults)
		}
		for _, m := range out {
			if tr, ok := m.(core.ToolResultMessage); ok && strings.HasPrefix(tr.Content.Text(), "No result") &&
				(!tr.IsError || tr.Content.Text() != anthropic.SyntheticResultText || tr.ToolName != "read") {
				t.Errorf("%s: synthetic result = %+v", c.name, tr)
			}
		}
	}
}

// Signed thinking is replayed only to the model that signed it; an unsigned
// block, another model's thinking and its thought signatures cannot be, and
// become text, or nothing when there is no text to keep.
func TestRepairKeepsThinkingOnlyWhereItsSignatureIsValid(t *testing.T) {
	signed := core.ThinkingBlock{Thinking: "reasoned", Signature: "sig"}
	redacted := core.ThinkingBlock{Redacted: true, Signature: "opaque"}
	sigCall := toolUse("c1", "read", `{}`)
	sigCall.ThoughtSignature = "ts"
	for _, c := range []struct {
		name   string
		model  string
		blocks core.Content
		want   core.Content
		report anthropic.RepairReport
	}{
		{"own signed thinking", "claude-test", core.Content{signed, redacted}, core.Content{signed, redacted}, anthropic.RepairReport{}},
		{"own unsigned thinking", "claude-test", core.Content{core.ThinkingBlock{Thinking: "partial"}, core.ThinkingBlock{}},
			core.Content{core.TextBlock{Text: "partial"}}, anthropic.RepairReport{DowngradedThinking: 2}},
		{"another model's", "gpt-x", core.Content{signed, redacted, core.TextBlock{Text: "t"}},
			core.Content{core.TextBlock{Text: "reasoned"}, core.TextBlock{Text: "t"}},
			anthropic.RepairReport{DowngradedThinking: 1, DroppedRedacted: 1}},
		{"another model's thought signature", "gpt-x", core.Content{sigCall}, core.Content{toolUse("c1", "read", `{}`)},
			anthropic.RepairReport{StrippedSignatures: 1}},
		{"own thought signature", "claude-test", core.Content{sigCall}, core.Content{sigCall}, anthropic.RepairReport{}},
	} {
		am := core.AssistantMessage{Model: c.model, Content: c.blocks, StopReason: core.StopReasonStop}
		in := core.Messages{am}
		if len(core.ExtractToolUse(&am)) > 0 {
			in = append(in, result("c1", "ok"))
		}
		out, rep := anthropic.RepairTranscript(in, anthropic.Target{Model: "claude-test"})
		if got := out[0].(core.AssistantMessage).Content; !reflect.DeepEqual(got, c.want) || rep != c.report {
			t.Errorf("%s: content %#v, report %+v; want %#v, %+v", c.name, got, rep, c.want, c.report)
		}
	}
}

// Another model's call ids are rewritten into this API's shape, and its
// results and any synthetic answer follow the rewrite; the target's own ids
// are left alone, and no two calls ever share an id.
func TestRepairRewritesOnlyAnotherModelsCallIDs(t *testing.T) {
	foreign := func(ids ...string) core.AssistantMessage {
		am := core.AssistantMessage{Model: "gpt-x", StopReason: core.StopReasonToolUse}
		for _, id := range ids {
			am.Content = append(am.Content, core.ToolUseBlock{ID: id, Name: "read", Input: []byte(`{}`)})
		}
		return am
	}
	long := strings.Repeat("x", 64)
	cases := []struct {
		name      string
		in        core.Messages
		ids       string // every tool_use id, then every result id
		rewritten int
	}{
		{"own ids", core.Messages{reply(toolUse("call:1", "read", `{}`)), result("call:1", "r")}, "call:1 | call:1", 0},
		{"foreign ids and their results", core.Messages{foreign("call:1"), result("call:1", "r")}, "call_1 | call_1", 1},
		{"a synthetic answer carries the rewrite", core.Messages{foreign("call:1")}, "call_1 | call_1", 1},
		{"a rewrite never lands on an own id", core.Messages{reply(toolUse("call_1", "read", `{}`)), result("call_1", "r"),
			foreign("call:1"), result("call:1", "r")}, "call_1,call_1_2 | call_1,call_1_2", 1},
		{"two foreign ids that normalize alike", core.Messages{foreign("a:b", "a.b"), result("a:b", "r"), result("a.b", "r")},
			"a_b,a_b_2 | a_b,a_b_2", 2},
		{"a suffix stays within 64 bytes", core.Messages{foreign(long+":", long+".")},
			long + "," + long[:62] + "_2 | " + long + "," + long[:62] + "_2", 2},
	}
	for _, c := range cases {
		out, rep := anthropic.RepairTranscript(c.in, self)
		var calls []string
		for _, m := range out {
			if am, ok := m.(core.AssistantMessage); ok {
				for _, tu := range core.ExtractToolUse(&am) {
					calls = append(calls, tu.ID)
				}
			}
		}
		if got := strings.Join(calls, ",") + " | " + resultIDs(out); got != c.ids || rep.RewrittenIDs != c.rewritten {
			t.Errorf("%s: ids %s, %d rewritten; want %s, %d", c.name, got, rep.RewrittenIDs, c.ids, c.rewritten)
		}
	}
}

// The repair is a view: its input is untouched, and repairing its output
// again changes nothing.
func TestRepairIsAViewAndAFixedPoint(t *testing.T) {
	build := func() core.Messages {
		return core.Messages{user("hi"),
			core.AssistantMessage{Model: "gpt-x", StopReason: core.StopReasonToolUse,
				Content: core.Content{core.ThinkingBlock{Thinking: "x", Signature: "s"}, core.ToolUseBlock{ID: "a:1", Name: "t", Input: []byte(`{}`)}}},
			reply(toolUse("b", "t", `{}`), core.ThinkingBlock{Thinking: "unsigned"})}
	}
	in := build()
	once, rep := anthropic.RepairTranscript(in, self)
	if !reflect.DeepEqual(in, build()) {
		t.Fatal("RepairTranscript changed its input")
	}
	if want := "synthetic results inserted, 2 thinking blocks downgraded, 1 tool-call ids rewritten"; !strings.HasSuffix(rep.String(), want) {
		t.Errorf("report = %q, want it to end %q", rep, want)
	}
	if again, rep2 := anthropic.RepairTranscript(once, self); rep2.Changed() || rep2.String() != "no repairs" || !reflect.DeepEqual(again, once) {
		t.Fatalf("a second pass made changes: %s", rep2)
	}
}

// Over every interleaving of user turns, calls, results and aborted turns the
// fuzzer builds, the repaired transcript is sendable.
func FuzzRepairAlwaysSendable(f *testing.F) {
	for _, seed := range []uint16{0x0000, 0xFFFF, 0xA5A5, 0x1964, 0x2486} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, shape uint16) {
		var in core.Messages
		for i := range 8 {
			id := "id" + string(rune('a'+i%4))
			switch (shape >> (i * 2)) & 0x3 {
			case 0:
				in = append(in, user("u"))
			case 1:
				in = append(in, reply(toolUse(id, "tool", `{}`)))
			case 2:
				in = append(in, core.ToolResultMessage{ToolUseID: id, ToolName: "tool"})
			case 3:
				in = append(in, core.AssistantMessage{Content: core.Content{toolUse(id, "tool", `{}`)},
					StopReason: core.StopReasonAborted, Model: "claude-test"})
			}
		}
		out, _ := anthropic.RepairTranscript(in, self)
		checkSendable(t, in, out)
	})
}
