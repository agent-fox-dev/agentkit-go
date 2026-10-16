package faux_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// TS-22-10: ToolCall equals the one-block tool_use literal it replaces.
// 22-REQ-3.1.
func TestToolCallEqualsTheLiteral_TS22_10(t *testing.T) {
	const id, name, args = "c1", "read_file", `{"path":"a"}`
	got := faux.ToolCall(id, name, args)
	want := faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall(id, name, args)}, StopReason: core.StopReasonToolUse}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolCall = %+v, want %+v", got, want)
	}
	if got.RawStopReason != "" || got.ErrorMessage != "" || got.Usage != (core.Usage{}) || got.Err != nil || got.Delay != 0 {
		t.Errorf("ToolCall left a field set besides Blocks and StopReason: %+v", got)
	}
}

// TS-22-11: Text equals the one-block text literal it replaces. 22-REQ-3.2.
func TestTextEqualsTheLiteral_TS22_11(t *testing.T) {
	for _, s := range []string{"", "hello", "two\nlines\n"} {
		got := faux.Text(s)
		want := faux.Turn{Blocks: []core.ContentBlock{core.TextBlock{Text: s}}, StopReason: core.StopReasonStop}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Text(%q) = %+v, want %+v", s, got, want)
		}
	}
}

// TS-22-12: ToolCall turns empty or whitespace args into the object {}.
// 22-REQ-3.3.
func TestToolCallEmptyArgs_TS22_12(t *testing.T) {
	for _, args := range []string{"", "  ", "\n\t"} {
		turn := faux.ToolCall("c", "t", args)
		if len(turn.Blocks) != 1 {
			t.Fatalf("ToolCall(%q) has %d blocks, want 1", args, len(turn.Blocks))
		}
		b, ok := turn.Blocks[0].(core.ToolUseBlock)
		if !ok {
			t.Fatalf("ToolCall(%q) block is %T, want core.ToolUseBlock", args, turn.Blocks[0])
		}
		if string(b.Input) != "{}" {
			t.Errorf("ToolCall(%q) input = %q, want {}", args, b.Input)
		}
	}
}

// TS-22-13: ToolCall panics, naming faux.ToolCall, the id and the tool, when
// args is not an object. 22-REQ-3.4.
func TestToolCallPanicsOnANonObject_TS22_13(t *testing.T) {
	for _, args := range []string{`[1]`, `"s"`, `12`, `{bad`} {
		msg, panicked := recoverString(func() { faux.ToolCall("id7", "my_tool", args) })
		if !panicked {
			t.Errorf("ToolCall with args %s did not panic", args)
			continue
		}
		for _, want := range []string{"faux.ToolCall", `"id7"`, `"my_tool"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic for args %s is %q, which lacks %s", args, msg, want)
			}
		}
	}
}

// recoverString runs f and returns the string it panicked with.
func recoverString(f func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			s, ok := r.(string)
			if !ok {
				s = fmt.Sprintf("non-string panic %T: %v", r, r)
			}
			msg, panicked = s, true
		}
	}()
	f()
	return "", false
}

// collect streams one request from p and returns every item, with the
// message timestamps zeroed: the provider stamps the time it ran, and nothing
// else in a turn varies between two providers scripted alike.
func collect(t *testing.T, p *faux.Provider) []core.StreamEvent {
	t.Helper()
	ch, err := p.Stream(context.Background(), core.Request{})
	if err != nil {
		t.Fatal(err)
	}
	var out []core.StreamEvent
	for item := range ch {
		switch e := item.Event.(type) {
		case core.MessageStartEvent:
			e.Message.Timestamp = time.Time{}
			item.Event = e
		case core.MessageUpdateEvent:
			e.Message.Timestamp = time.Time{}
			item.Event = e
		case core.MessageEndEvent:
			e.Message.Timestamp = time.Time{}
			item.Event = e
		}
		out = append(out, item)
	}
	return out
}

// TS-22-14 (integration): a Provider scripted with the helpers emits the same
// events as one scripted with the literals. 22-REQ-3.5.
func TestProviderEmitsTheSameEvents_TS22_14(t *testing.T) {
	const args = `{"path":"a"}`
	withHelpers := faux.New(faux.ToolCall("c1", "read_file", args), faux.Text("done"))
	withLiterals := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "read_file", args)}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	)
	for turn := 1; turn <= 2; turn++ {
		a, b := collect(t, withHelpers), collect(t, withLiterals)
		if len(a) < 6 {
			t.Fatalf("turn %d: only %d items were collected: %+v", turn, len(a), a)
		}
		if !reflect.DeepEqual(a, b) {
			t.Errorf("turn %d: the helpers emitted\n%+v\nthe literals emitted\n%+v", turn, a, b)
		}
	}
}
