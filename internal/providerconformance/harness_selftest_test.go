package providerconformance

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// helloCase is the smallest case: one text turn. The cases proper are in the
// files that follow; this one exercises the harness alone.
var helloCase = conformanceCase{
	name:   "hello",
	prompt: "say hello",
	turns:  func(providerInfo) []logicalTurn { return []logicalTurn{oneText("hello")} },
}

// TS-30-6 (30-REQ-1.6): a fake that never answers does not hang the case. The
// run is cancelled at its deadline and the failure names the provider and the
// case, and a healthy case alongside is not affected.
func TestHangingFakeFailsTheCaseNamedAndInTime_TS30_6(t *testing.T) {
	p := providerByName("ollama")
	hung := &recorder{}
	start := time.Now()
	runCase(hung, p, helloCase, runOptions{timeout: 200 * time.Millisecond, hang: true})
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("runCase took %v against a fake that never answers, want it to return soon after its 200ms deadline", took)
	}
	msgs := hung.all()
	if len(msgs) == 0 {
		t.Fatal("a case whose fake never answered was not reported as a failure")
	}
	if !strings.Contains(msgs[0], "ollama") || !strings.Contains(msgs[0], "hello") {
		t.Errorf("the failure %q does not name the provider ollama and the case hello", msgs[0])
	}
	if !strings.Contains(msgs[0], "did not finish within 200ms") || !strings.Contains(msgs[0], "cancelled") {
		t.Errorf("the first failure %q does not say the run did not finish within its 200ms and was cancelled", msgs[0])
	}

	healthy := &recorder{}
	runCase(healthy, p, helloCase, runOptions{})
	if healthy.failed() {
		t.Errorf("the healthy case failed: %v", healthy.all())
	}
}

// TS-30-16 (30-REQ-3.5): a request count that differs from the script fails the
// case, in either direction, naming both counts.
func TestCheckCountNamesBothCounts_TS30_16(t *testing.T) {
	for _, c := range []struct {
		scripted, recorded int
		fails              bool
	}{{2, 1, true}, {1, 2, true}, {3, 0, true}, {2, 2, false}, {0, 0, false}} {
		rec := &recorder{}
		checkCount(rec, "ollama/x", c.scripted, c.recorded)
		msgs := rec.all()
		if c.fails != (len(msgs) == 1) {
			t.Errorf("scripted %d, recorded %d: reported %q, want a failure: %v", c.scripted, c.recorded, msgs, c.fails)
			continue
		}
		if c.fails {
			want := "scripts " + string(rune('0'+c.scripted)) + " requests and the fake recorded " + string(rune('0'+c.recorded))
			if !strings.Contains(msgs[0], want) || !strings.Contains(msgs[0], "ollama/x") {
				t.Errorf("the failure %q does not name the case and say %q", msgs[0], want)
			}
		}
	}
}

// The same through a run: a script the agent does not use up, and an agent that
// asks for more than the script holds, each fail the case for every provider.
func TestRunCaseFailsOnAnUnusedOrExceededScript_TS30_16(t *testing.T) {
	unused := conformanceCase{
		name: "unused", prompt: "go",
		turns: func(providerInfo) []logicalTurn { return []logicalTurn{oneText("a"), oneText("b")} },
	}
	exceeded := conformanceCase{
		name: "exceeded", prompt: "go",
		turns: func(providerInfo) []logicalTurn {
			return []logicalTurn{{blocks: []block{toolBlock("call_1", "echo", `{"v":"x"}`)}, end: endToolStop}}
		},
	}
	for _, p := range providerTable {
		for _, c := range []struct {
			c        conformanceCase
			scripted int
			recorded int
		}{{unused, 2, 1}, {exceeded, 1, 2}} {
			rec := &recorder{}
			runCase(rec, p, c.c, runOptions{})
			want := "scripts " + string(rune('0'+c.scripted)) + " requests and the fake recorded " + string(rune('0'+c.recorded))
			found := false
			for _, m := range rec.all() {
				found = found || (strings.Contains(m, want) && strings.Contains(m, p.name+"/"+c.c.name))
			}
			if !found {
				t.Errorf("%s/%s: reported %q, want a failure saying %q", p.name, c.c.name, rec.all(), want)
			}
		}
	}
}

// requestBody is a request body in the vendor's form that says nothing but the
// model and the effort.
func requestBody(v vendor, model, effort string) []byte {
	switch v.name {
	case "anthropic":
		return []byte(`{"model":"` + model + `","output_config":{"effort":"` + effort + `"}}`)
	case "openrouter":
		return []byte(`{"model":"` + model + `","reasoning":{"effort":"` + effort + `"}}`)
	}
	return []byte(`{"model":"` + model + `","think":"` + effort + `"}`)
}

// TS-30-13's check, at its source: every request is compared with the selected
// row, a wrong model and a wrong effort each fail, and a right pair passes.
func TestCommonChecksCompareEveryRequestWithTheSelectedRow_TS30_13(t *testing.T) {
	for _, p := range providerTable {
		sel, err := catalog.Select(p.name, "STANDARD", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			name, model, effort string
			want                string // "" for none, else the word the failure carries
		}{
			{"right", sel.Model.ID, string(sel.Effort), ""},
			{"wrong model", "some-other-model", string(sel.Effort), "model"},
			{"wrong effort", sel.Model.ID, "minimal", "effort"},
			{"no effort", sel.Model.ID, "", "effort"},
		} {
			rec := &recorder{}
			f := newFake(rec, p.vendor, oneText("x"), oneText("y"))
			post(t, f.Endpoint(), nil, requestBody(p.vendor, sel.Model.ID, string(sel.Effort)))
			post(t, f.Endpoint(), nil, requestBody(p.vendor, c.model, c.effort))
			r := &caseRun{tb: rec, name: p.name + "/x", p: p, sel: sel, fake: f}
			r.checkCommon()
			f.Close()
			msgs := rec.all()
			switch {
			case c.want == "" && len(msgs) != 0:
				t.Errorf("%s %s: reported %q for a request that matches the row", p.name, c.name, msgs)
			case c.want != "" && (len(msgs) != 1 || !strings.Contains(msgs[0], "request 2") || !strings.Contains(msgs[0], c.want)):
				t.Errorf("%s %s: reported %q, want one failure about the %s of request 2", p.name, c.name, msgs, c.want)
			}
		}
	}
}

// A request the vendor's decoder cannot read is a failure of the case, naming
// the request, not a request skipped.
func TestCommonChecksReportAnUndecodableRequest_TS30_13(t *testing.T) {
	p := providerByName("openrouter")
	sel, err := catalog.Select(p.name, "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	f := newFake(rec, p.vendor, oneText("x"), oneText("y"))
	defer f.Close()
	post(t, f.Endpoint(), nil, requestBody(p.vendor, sel.Model.ID, string(sel.Effort)))
	post(t, f.Endpoint(), nil, []byte(`{"model":`))
	r := &caseRun{tb: rec, name: p.name + "/x", p: p, sel: sel, fake: f}
	r.checkCommon()
	if msgs := rec.all(); len(msgs) != 1 || !strings.Contains(msgs[0], "request 2") {
		t.Errorf("reported %q, want one failure naming request 2", msgs)
	}
}

// A block-end event of another kind than the scripted block is named for what it
// is, even when everything else about it matches the scripted block.
func TestNormalTurnReportsABlockOfAnotherKind_TS30_14(t *testing.T) {
	tool := toolBlock("call_1", "echo", `{"v":"x"}`)
	asText := tool
	asText.kind = blockText
	for _, c := range []struct {
		name   string
		got    block // what the provider streamed
		script block // what the case scripted
		want   string
	}{
		{"text for thinking", textBlock("Same"), thinkingBlock("Same", ""), "TextEndEvent"},
		{"thinking for text", thinkingBlock("Same", ""), textBlock("Same"), "ThinkingEndEvent"},
		{"tool call for text", tool, asText, "ToolCallEndEvent"},
	} {
		evs, err := fauxEvents(logicalTurn{blocks: []block{c.got}})
		if err != nil {
			t.Fatal(err)
		}
		rec := &recorder{}
		r := &caseRun{tb: rec, name: "x/y"}
		r.checkNormalTurn(0, logicalTurn{blocks: []block{c.script}}, evs)
		if !strings.Contains(strings.Join(rec.all(), "\n"), c.want) {
			t.Errorf("%s: reported %q, want a failure naming the %s", c.name, rec.all(), c.want)
		}
	}
}

// A turn with a block of every kind and a tool-call stop, and the events
// provider/faux emits for it.
func mixedTurn() logicalTurn {
	return logicalTurn{
		blocks: []block{textBlock("Hello"), thinkingBlock("Plan", "sig"), toolBlock("call_1", "echo", `{"v":"x"}`)},
		end:    endToolStop,
	}
}

func mixedEvents(t *testing.T) []core.Event {
	t.Helper()
	evs, err := fauxEvents(mixedTurn())
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// check runs checkNormalTurn on evs and returns what it reported.
func check(evs []core.Event) []string {
	rec := &recorder{}
	r := &caseRun{tb: rec, name: "x/y"}
	r.checkNormalTurn(0, mixedTurn(), evs)
	return rec.all()
}

// without returns evs less the first event that satisfies drop.
func without(evs []core.Event, drop func(core.Event) bool) []core.Event {
	out := slices.Clone(evs)
	for i, e := range out {
		if drop(e) {
			return slices.Delete(out, i, i+1)
		}
	}
	return out
}

// TS-30-14's checks, at their source: the faux events pass; an event missing,
// out of order or carrying a payload that is not whole fails; and what the
// skeleton leaves out (snapshots, the count of deltas) changes nothing.
func TestNormalTurnChecks_TS30_14(t *testing.T) {
	evs := mixedEvents(t)
	if msgs := check(evs); len(msgs) != 0 {
		t.Fatalf("the faux events themselves failed the check: %v", msgs)
	}

	// Allowed: an extra snapshot and an extra delta of a block already streaming.
	extra := slices.Clone(evs)
	for i, e := range extra {
		if d, ok := e.(core.TextDeltaEvent); ok {
			extra = slices.Insert(extra, i, core.Event(core.TextDeltaEvent{BlockIndex: d.BlockIndex, Delta: "!"}), core.Event(core.MessageUpdateEvent{}))
			break
		}
	}
	if msgs := check(extra); len(msgs) != 0 {
		t.Errorf("an extra delta and an extra snapshot failed the check: %v", msgs)
	}

	for _, c := range []struct {
		name string
		evs  []core.Event
		want string
	}{
		{"no thinking_end", without(evs, func(e core.Event) bool { _, ok := e.(core.ThinkingEndEvent); return ok }), "skeleton"},
		{"no message_end", without(evs, func(e core.Event) bool { _, ok := e.(core.MessageEndEvent); return ok }), "skeleton"},
		{"no tool_call_start", without(evs, func(e core.Event) bool { _, ok := e.(core.ToolCallStartEvent); return ok }), "skeleton"},
		{"no text_delta", without(evs, func(e core.Event) bool { _, ok := e.(core.TextDeltaEvent); return ok }), "skeleton"},
		{"two blocks' deltas swapped", swapFirstTwo(evs), "skeleton"},
		{"text end is not whole", replaceEnd(evs, core.TextEndEvent{BlockIndex: 0, Text: "Hel"}), "TextEndEvent"},
		{"thinking end is not whole", replaceEnd(evs, core.ThinkingEndEvent{BlockIndex: 1, Thinking: "Pl"}), "ThinkingEndEvent"},
		{"tool end has other arguments", replaceEnd(evs, core.ToolCallEndEvent{BlockIndex: 2, Block: core.ToolUseBlock{ID: "call_1", Name: "echo", Input: []byte(`{"v":"y"}`)}}), "ToolCallEndEvent"},
		{"tool end has another id", replaceEnd(evs, core.ToolCallEndEvent{BlockIndex: 2, Block: core.ToolUseBlock{ID: "call_9", Name: "echo", Input: []byte(`{"v":"x"}`)}}), "ToolCallEndEvent"},
		{"tool end has another name", replaceEnd(evs, core.ToolCallEndEvent{BlockIndex: 2, Block: core.ToolUseBlock{ID: "call_1", Name: "other", Input: []byte(`{"v":"x"}`)}}), "ToolCallEndEvent"},
	} {
		msgs := check(c.evs)
		if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), c.want) {
			t.Errorf("%s: reported %q, want a failure mentioning %s", c.name, msgs, c.want)
		}
	}
}

// swapFirstTwo swaps the first text_delta with the thinking_start that follows
// its block.
func swapFirstTwo(evs []core.Event) []core.Event {
	out := slices.Clone(evs)
	a, b := -1, -1
	for i, e := range out {
		switch e.(type) {
		case core.TextDeltaEvent:
			if a < 0 {
				a = i
			}
		case core.ThinkingStartEvent:
			if b < 0 {
				b = i
			}
		}
	}
	out[a], out[b] = out[b], out[a]
	return out
}

// replaceEnd replaces the block-end event of the same type as end.
func replaceEnd(evs []core.Event, end core.Event) []core.Event {
	out := slices.Clone(evs)
	for i, e := range out {
		if e.EventType() == end.EventType() {
			out[i] = end
		}
	}
	return out
}

// TS-30-15's check, at its source: a terminal ErrorEvent then a MessageEndEvent
// with StopReasonError pass, and anything else fails.
func TestFailedTurnChecks_TS30_15(t *testing.T) {
	end := func(reason core.StopReason) core.Event {
		return core.MessageEndEvent{Message: core.AssistantMessage{StopReason: reason}}
	}
	boom := core.ErrorEvent{Message: "boom", Terminal: true}
	for _, c := range []struct {
		name string
		evs  []core.Event
		ok   bool
	}{
		{"error then end", []core.Event{core.MessageStartEvent{}, boom, end(core.StopReasonError)}, true},
		{"just the pair", []core.Event{boom, end(core.StopReasonError)}, true},
		{"error not terminal", []core.Event{core.ErrorEvent{Message: "boom"}, end(core.StopReasonError)}, false},
		{"end first", []core.Event{end(core.StopReasonError), boom}, false},
		{"stop reason stop", []core.Event{boom, end(core.StopReasonStop)}, false},
		{"stop reason aborted", []core.Event{boom, end(core.StopReasonAborted)}, false},
		{"no error", []core.Event{core.MessageStartEvent{}, end(core.StopReasonError)}, false},
		{"only the end", []core.Event{end(core.StopReasonError)}, false},
		{"nothing", nil, false},
	} {
		rec := &recorder{}
		r := &caseRun{tb: rec, name: "x/y"}
		r.checkFailedTurn(0, c.evs)
		if c.ok != !rec.failed() {
			t.Errorf("%s: reported %q, want a pass: %v", c.name, rec.all(), c.ok)
		}
	}
}

// The window of a turn's provider events, and the skeleton, on events built by
// hand.
func TestProviderEventsAndSkeleton(t *testing.T) {
	end := core.MessageEndEvent{}
	events := []core.Event{
		core.AgentStartEvent{}, core.TurnStartEvent{TurnIndex: 0},
		core.MessageStartEvent{}, core.TextStartEvent{BlockIndex: 0}, core.TextDeltaEvent{BlockIndex: 0, Delta: "a"},
		core.TextDeltaEvent{BlockIndex: 0, Delta: "b"}, core.MessageUpdateEvent{}, core.TextEndEvent{BlockIndex: 0, Text: "ab"}, end,
		core.ToolExecutionStartEvent{}, core.TurnEndEvent{TurnIndex: 0},
		core.TurnStartEvent{TurnIndex: 1}, core.MessageStartEvent{}, end, core.TurnEndEvent{TurnIndex: 1},
	}
	for _, c := range []struct {
		turn int
		want int // how many events
	}{{0, 7}, {1, 2}, {2, 0}, {-1, 0}} {
		if got := providerEvents(events, c.turn); len(got) != c.want {
			t.Errorf("providerEvents(turn %d) holds %d events, want %d", c.turn, len(got), c.want)
		}
	}
	// An unfinished turn runs to the end of what was seen.
	if got := providerEvents(events[:5], 0); len(got) != 3 {
		t.Errorf("an unfinished turn holds %d events, want the 3 seen", len(got))
	}
	want := []string{"message_start", "text_start", "text_delta", "text_end", "message_end"}
	if got := skeleton(providerEvents(events, 0)); !slices.Equal(got, want) {
		t.Errorf("skeleton = %v, want %v", got, want)
	}
	// Deltas of two blocks are two runs, even with nothing between them but the
	// snapshots the skeleton drops.
	two := []core.Event{
		core.TextDeltaEvent{BlockIndex: 0}, core.TextDeltaEvent{BlockIndex: 0}, core.MessageUpdateEvent{},
		core.TextDeltaEvent{BlockIndex: 1}, core.ThinkingDeltaEvent{BlockIndex: 1}, core.ThinkingDeltaEvent{BlockIndex: 1},
		core.ToolInputDeltaEvent{BlockIndex: 2}, core.ToolInputDeltaEvent{BlockIndex: 2},
	}
	more := []core.Event{
		core.ThinkingDeltaEvent{BlockIndex: 0}, core.ThinkingDeltaEvent{BlockIndex: 1},
		core.ToolInputDeltaEvent{BlockIndex: 2}, core.ToolInputDeltaEvent{BlockIndex: 3},
	}
	if got := skeleton(more); len(got) != 4 {
		t.Errorf("skeleton of one delta in each of two blocks of a kind = %v, want four events", got)
	}
	wantTwo := []string{"text_delta", "text_delta", "thinking_delta", "tool_input_delta"}
	if got := skeleton(two); !slices.Equal(got, wantTwo) {
		t.Errorf("skeleton of deltas = %v, want %v", got, wantTwo)
	}
}

// A case's own check runs after the run, with the run in hand, and what it
// reports is reported under the provider and the case.
func TestCaseCheckRunsWithTheRun(t *testing.T) {
	var seen *caseRun
	c := helloCase
	c.name = "withcheck"
	c.check = func(r *caseRun) {
		seen = r
		r.fail("the case's own failure")
	}
	rec := &recorder{}
	r := runCase(rec, providerByName("ollama"), c, runOptions{})
	msgs := rec.all()
	if len(msgs) != 1 || msgs[0] != "ollama/withcheck: the case's own failure" {
		t.Fatalf("reported %q, want exactly the case's own failure, named by provider and case", msgs)
	}
	if seen != r || r.res.FinalText() != "hello" || len(r.events) == 0 || len(r.requests) != 1 {
		t.Errorf("the check saw run %p (want %p) with final text %q, %d events and %d requests", seen, r, r.res.FinalText(), len(r.events), len(r.requests))
	}
}

// The harness itself, end to end: the smallest case passes its common checks on
// every provider, with the run it recorded to show for it.
func TestHelloCasePassesOnEveryProvider(t *testing.T) {
	for _, p := range providerTable {
		rec := &recorder{}
		r := runCase(rec, p, helloCase, runOptions{})
		if rec.failed() {
			t.Errorf("%s: %v", p.name, rec.all())
		}
		if r.res.FinalText() != "hello" || len(r.requests) != 1 || len(r.summaries) != 1 || len(r.events) == 0 {
			t.Errorf("%s: final text %q, %d requests, %d summaries, %d events; want hello, 1, 1 and some events",
				p.name, r.res.FinalText(), len(r.requests), len(r.summaries), len(r.events))
		}
		if r.cfg.Provider != p.name || r.cfg.Model != "STANDARD" || r.cfg.MaxTurns != 4 || len(r.cfg.Toolset.Tools) != 1 || r.cfg.Toolset.Tools[0].Name != "echo" {
			t.Errorf("%s: the agent was built from %+v, want Provider %s, Model STANDARD, MaxTurns 4 and the tool echo", p.name, r.cfg, p.name)
		}
		if got, want := slices.Sorted(maps.Keys(r.env.vars)), slices.Sorted(maps.Keys(p.vendor.env("x"))); !slices.Equal(got, want) {
			t.Errorf("%s: the environment holds %v, want only %v", p.name, got, want)
		}
		if len(r.env.Asked()) == 0 {
			t.Errorf("%s: the provider read no variable at all", p.name)
		}
	}
}
