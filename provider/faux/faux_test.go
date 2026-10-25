package faux_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// streamOne runs one request through p and returns its events and result.
func streamOne(t *testing.T, ctx context.Context, p *faux.Provider) ([]core.Event, core.StreamResult) {
	t.Helper()
	s := core.EventStreamOf(p.Stream(ctx, core.Request{}))
	var events []core.Event
	for e := range s.Events() {
		events = append(events, e)
	}
	return events, s.Wait()
}

func TestTurnHelpersEqualTheLiteralsTheyReplace(t *testing.T) {
	call := faux.FauxToolCall("c1", "read_file", `{"path":"a"}`)
	cases := []struct {
		name      string
		got, want faux.Turn
	}{
		{"ToolCall", faux.ToolCall("c1", "read_file", `{"path":"a"}`),
			faux.Turn{Blocks: []core.ContentBlock{call}, StopReason: core.StopReasonToolUse}},
		{"Text", faux.Text("two\nlines"),
			faux.Turn{Blocks: []core.ContentBlock{core.TextBlock{Text: "two\nlines"}}, StopReason: core.StopReasonStop}},
		{"FauxAssistantMessage", faux.FauxAssistantMessage(core.StopReasonLength, faux.FauxText("a"), faux.FauxThinking("t", "sig")),
			faux.Turn{Blocks: []core.ContentBlock{core.TextBlock{Text: "a"}, core.ThinkingBlock{Thinking: "t", Signature: "sig"}},
				StopReason: core.StopReasonLength}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	for _, args := range []string{"", "  ", "\n\t"} {
		if b := faux.ToolCall("c", "t", args).Blocks[0].(core.ToolUseBlock); string(b.Input) != "{}" {
			t.Errorf("ToolCall(%q) input = %q, want {}", args, b.Input)
		}
	}
}

func TestToolCallHelpersPanicNamingTheCallOnANonObject(t *testing.T) {
	for name, f := range map[string]func(string){
		"faux.ToolCall":     func(a string) { faux.ToolCall("id7", "my_tool", a) },
		"faux.FauxToolCall": func(a string) { faux.FauxToolCall("id7", "my_tool", a) },
	} {
		for _, args := range []string{`[1]`, `"s"`, `12`, `{bad`} {
			func() {
				defer func() {
					msg, _ := recover().(string)
					for _, want := range []string{name + "(", `"id7"`, `"my_tool"`} {
						if !strings.Contains(msg, want) {
							t.Errorf("%s(%s) panicked with %q, which lacks %s", name, args, msg, want)
						}
					}
				}()
				f(args)
			}()
		}
	}
}

// The event order is the protocol's specification: every block streams, then
// every block ends in order, then the message ends.
func TestTheProviderEmitsTheNormativeEventOrder(t *testing.T) {
	p := faux.New(faux.FauxAssistantMessage(core.StopReasonToolUse,
		faux.FauxThinking("plan", "sig"), faux.FauxText("Hello"), faux.FauxToolCall("c1", "edit", `{"a":1}`)))
	p.ModelID = "faux-1"
	events, res := streamOne(t, context.Background(), p)

	const want = "message_start," +
		"thinking_start,thinking_delta,message_update," +
		"text_start,text_delta,message_update," +
		"tool_call_start,tool_input_delta,message_update," +
		"thinking_end,text_end,tool_call_end,message_end"
	if got := faux.EventNames(events); got != want {
		t.Fatalf("events\n got %s\nwant %s", got, want)
	}
	if res.Err != nil || res.Message.StopReason != core.StopReasonToolUse || res.Message.RawStopReason != "tool_use" ||
		res.Message.Model != "faux-1" || len(res.Message.Content) != 3 {
		t.Fatalf("result = %+v, %v", res.Message, res.Err)
	}
	if end := events[11].(core.TextEndEvent); end.Text != "Hello" {
		t.Errorf("text end = %+v, want the whole text", end)
	}
}

func TestChunkSizeSplitsTextAndArgumentsIntoDeltas(t *testing.T) {
	p := faux.New(faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxText("abcde"), faux.FauxToolCall("c", "t", `{"k":1}`)))
	p.ChunkSize = 2
	events, _ := streamOne(t, context.Background(), p)
	var text, args []string
	for _, e := range events {
		switch d := e.(type) {
		case core.TextDeltaEvent:
			text = append(text, d.Delta)
		case core.ToolInputDeltaEvent:
			args = append(args, d.Delta)
		}
	}
	if strings.Join(text, "|") != "ab|cd|e" || strings.Join(args, "|") != `{"|k"|:1|}` {
		t.Errorf("deltas = %q / %q", text, args)
	}
}

func TestTheProviderReplaysItsScriptThenAnswersEmptyStops(t *testing.T) {
	p := faux.New(faux.Text("one"), faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("two")}, RawStopReason: "end_turn"})
	var got []string
	for range 3 {
		_, res := streamOne(t, context.Background(), p)
		got = append(got, res.Message.Content.Text()+"/"+string(res.Message.StopReason)+"/"+res.Message.RawStopReason)
	}
	if want := "one/stop/stop two/stop/end_turn /stop/stop"; strings.Join(got, " ") != want {
		t.Errorf("turns = %q, want %q", strings.Join(got, " "), want)
	}
	if p.Calls() != 3 || len(p.Requests()) != 3 {
		t.Errorf("Calls = %d, Requests = %d, want 3", p.Calls(), len(p.Requests()))
	}

	var zero faux.Provider
	if _, res := streamOne(t, context.Background(), &zero); res.Message.StopReason != core.StopReasonStop {
		t.Errorf("the zero provider answered %+v", res.Message)
	}
	if m := faux.Model(); m.ID == "" || m.ContextWindow <= 0 || m.MaxOutputTokens <= 0 {
		t.Errorf("Model() = %+v", m)
	}
}

func TestAScriptedErrorEndsTheTurnAfterItsBlocks(t *testing.T) {
	boom := errors.New("connection reset")
	p := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("half")}, Err: boom})
	events, res := streamOne(t, context.Background(), p)
	if !errors.Is(res.Err, boom) || res.Message.StopReason != core.StopReasonError ||
		res.Message.ErrorMessage != "connection reset" || res.Message.Content.Text() != "half" {
		t.Fatalf("result = %+v, %v", res.Message, res.Err)
	}
	if !strings.HasSuffix(faux.EventNames(events), "text_end,error,message_end") {
		t.Errorf("events = %s", faux.EventNames(events))
	}
}

func TestCancellationAbortsTheTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := faux.New(faux.Text("never"))
	if _, err := p.Stream(ctx, core.Request{}); !errors.Is(err, context.Canceled) || p.Calls() != 0 {
		t.Errorf("a done context: err = %v, calls = %d; want it refused before it counts", err, p.Calls())
	}

	// Cancelled during the scripted delay: the turn ends aborted.
	ctx, cancel = context.WithCancel(context.Background())
	p = faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("late")}, Delay: time.Hour})
	ch, err := p.Stream(ctx, core.Request{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	res := core.EventStreamOf(ch, nil).Wait()
	if !errors.Is(res.Err, context.Canceled) || res.Message.StopReason != core.StopReasonAborted || len(res.Message.Content) != 0 {
		t.Errorf("result = %+v, %v; want an aborted, empty turn", res.Message, res.Err)
	}
}
