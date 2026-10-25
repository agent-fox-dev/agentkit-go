package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

func drain(ch <-chan core.StreamEvent) []core.StreamEvent {
	var out []core.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func ended(events []core.Event, res core.StreamResult) *core.EventStream {
	s := core.NewEventStream(core.StreamOptions{})
	for _, e := range events {
		s.Push(e)
	}
	s.End(res)
	return s
}

func TestStreamChannelSendsEveryEventThenTheResult(t *testing.T) {
	boom := errors.New("boom")
	msg := core.AssistantMessage{StopReason: core.StopReasonStop, Content: core.Content{core.TextBlock{Text: "done"}}}
	for _, tc := range []struct {
		name      string
		stream    *core.EventStream
		n         int  // items on the channel
		lastIsEnd bool // the last item carries a MessageEndEvent
		err       error
	}{
		{"the last event is the result", ended([]core.Event{core.TextDeltaEvent{}, core.MessageEndEvent{Message: msg}}, core.StreamResult{Message: &msg}), 2, true, nil},
		{"the result was not pushed", ended([]core.Event{core.TextDeltaEvent{}}, core.StreamResult{Message: &msg}), 2, true, nil},
		{"a failed stream", ended([]core.Event{core.MessageEndEvent{Message: msg}}, core.StreamResult{Message: &msg, Err: boom}), 2, false, boom},
		{"no result at all", ended(nil, core.StreamResult{}), 0, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := drain(core.StreamChannel(tc.stream))
			if len(got) != tc.n {
				t.Fatalf("%d items, want %d: %+v", len(got), tc.n, got)
			}
			if tc.n == 0 {
				return
			}
			last := got[len(got)-1]
			if _, isEnd := last.Event.(core.MessageEndEvent); isEnd != tc.lastIsEnd || last.Err != tc.err {
				t.Fatalf("last item = %+v, want end=%v err=%v", last, tc.lastIsEnd, tc.err)
			}
		})
	}
}

func TestEventStreamOfReadsAProvidersChannel(t *testing.T) {
	boom := errors.New("boom")
	if s := core.EventStreamOf(nil, boom); s.Err() != boom || s.Result().StopReason != core.StopReasonError {
		t.Fatalf("Stream's own error = %v, %+v", s.Err(), s.Result())
	}
	if res := core.EventStreamOf(nil, nil).Wait(); res.Err == nil {
		t.Fatal("a provider that returned no channel and no error must end the stream with an error")
	}
	ch := make(chan core.StreamEvent, 4)
	msg := core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "done"}}}
	ch <- core.StreamEvent{Event: core.TextDeltaEvent{Delta: "d"}}
	ch <- core.StreamEvent{Event: core.MessageEndEvent{Message: msg}}
	ch <- core.StreamEvent{Err: boom}
	close(ch)
	s := core.EventStreamOf(ch, nil)
	var n int
	for range s.Events() {
		n++
	}
	if res := s.Wait(); n != 2 || res.Err != boom || res.Message == nil || res.Message.Content.Text() != "done" {
		t.Fatalf("%d events, result %+v", n, res)
	}
	// The round trip through a channel keeps the stream as it was.
	again := core.EventStreamOf(core.StreamChannel(ended([]core.Event{core.MessageEndEvent{Message: msg}}, core.StreamResult{Message: &msg})), nil)
	if got := again.Result(); got == nil || got.Content.Text() != "done" || again.Err() != nil {
		t.Fatalf("round trip = %+v, %v", got, again.Err())
	}
}

// A Stream on a context that is already done fails at once: with an error,
// or an error on the channel's last item.
func TestProviderStreamOnADoneContextFails(t *testing.T) {
	m, _ := catalog.Lookup("claude-opus-5-5")
	clients := map[string]core.ProviderClient{
		"faux":      faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("never"))),
		"anthropic": anthropic.Provider(m, anthropic.Options{Getenv: func(string) string { return "" }}),
	}
	for name, client := range clients {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if s := core.EventStreamOf(client.Stream(ctx, core.Request{})); s.Wait().Err == nil {
			t.Errorf("%s: a stream on a cancelled context ended without an error", name)
		}
	}
}

func TestProviderStreamDeliversTheTurnOnTheChannel(t *testing.T) {
	tools := []core.Tool{{Name: "read_file", Description: "reads"}}
	var client core.ProviderClient = faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")))
	s := core.EventStreamOf(client.Stream(context.Background(), core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: "system"}}, Tools: core.ToolWires(tools)}))
	if m := s.Result(); m == nil || m.Content.Text() != "done" || m.StopReason != core.StopReasonStop || s.Err() != nil {
		t.Fatalf("final message = %+v, %v", m, s.Err())
	}
}
