package ollama_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

func TestStreamRefusesAContextAlreadyDone(t *testing.T) {
	srv := newServer(t, okTurn())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ch, err := newClient(t, srv, testModel()).Stream(ctx, userRequest("hi")); ch != nil || !errors.Is(err, context.Canceled) || srv.Count() != 0 {
		t.Errorf("Stream = %v, %v after %d requests; want no channel, context.Canceled and none", ch, err, srv.Count())
	}
}

// Cancelling mid-stream ends the turn as aborted: the text so far, no tool
// call and no ErrorEvent.
func TestCancellingMidStreamAbortsTheTurn(t *testing.T) {
	srv := newServer(t, reply{status: http.StatusOK, body: chunk("hello", "") + "\n" + toolChunk("call_1", "f", `{"a":1}`) + "\n", hold: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := newClient(t, srv, testModel()).Stream(ctx, userRequest("hi"))
	if err != nil {
		t.Fatal(err)
	}
	for ev := range ch {
		if _, ok := ev.Event.(core.TextDeltaEvent); ok {
			break
		}
	}
	cancel()
	ended := make(chan outcome, 1)
	go func() { ended <- drain(ch) }()
	var o outcome
	select {
	case o = <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not end after the cancel")
	}
	if o.err != core.ErrAborted || o.msg.StopReason != core.StopReasonAborted || o.msg.ErrorMessage != "Request was aborted" ||
		o.msg.Content.Text() != "hello" || len(core.ExtractToolUse(&o.msg)) != 0 || strings.Contains(kinds(o.events), "error") {
		t.Errorf("turn = %v, %q %q %#v, events %s; want an aborted turn with the text only", o.err, o.msg.StopReason, o.msg.ErrorMessage, o.msg.Content, kinds(o.events))
	}
}

// Concurrent streams on one Client, all in flight at once, each come back
// whole: the thinking, the text in three pieces and the id-less tool call of
// one response, with an id no other stream got.
func TestConcurrentStreamsOnOneClientAreIndependent(t *testing.T) {
	const streams = 32
	var arrived atomic.Int32
	all := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := arrived.Add(1); n == streams {
			close(all)
		}
		<-all
		mark := fmt.Sprintf("mark-%p", r)
		for _, line := range []string{chunk("", mark), chunk(mark[:3], ""), chunk(mark[3:7], ""), chunk(mark[7:], ""),
			toolChunk("", "f", fmt.Sprintf(`{"m":%q}`, mark)), doneChunk("m", "stop")} {
			fmt.Fprintln(w, line)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, &fakeServer{srv: srv}, testModel())
	outs := make([]outcome, streams)
	var wg sync.WaitGroup
	for i := range streams {
		wg.Go(func() {
			if ch, err := c.Stream(context.Background(), userRequest("hi")); err == nil {
				outs[i] = drain(ch)
			}
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, o := range outs {
		m := o.msg
		calls := core.ExtractToolUse(&m)
		if o.err != nil || len(m.Content) != 3 || len(calls) != 1 || m.Content[0].(core.ThinkingBlock).Thinking != m.Content.Text() ||
			string(calls[0].Input) != fmt.Sprintf(`{"m":%q}`, m.Content.Text()) || seen[m.Content.Text()] || seen[calls[0].ID] {
			t.Errorf("stream %d: %v, %#v: parts of different responses, or another stream's", i, o.err, m.Content)
		}
		seen[m.Content.Text()], seen[calls[0].ID] = true, true
	}
}

// An agent completes a two-turn tool loop: both requests carry the catalog
// row's model, window and effort and the tool, the second replays the call's
// id, its arguments in the server's key order, the thinking, and the result;
// the run costs 0.
func TestATwoTurnToolLoopRunsAtZeroCost(t *testing.T) {
	const args = `{"zeta":"z","mu":"m","alpha":"a","omega":"o","beta":"b"}`
	srv := newServer(t,
		lines(chunk("", "I should read the file"), toolChunk("call_1", "read_file", args), usageDone(354, 256, 120)),
		lines(chunk("The file says: file contents.", ""), usageDone(400, 300, 50)))
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ollama.New(sel.Model, ollama.Options{Env: map[string]string{ollama.HostVar: srv.URL()}})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	tool := core.Tool{Name: "read_file", Description: "reads a file", Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		ran++
		return json.RawMessage(`{"text":"file contents"}`), nil
	}}
	agent, err := agentkit.New(agentkit.Config{Client: p, Model: sel.Model.ID, Effort: sel.Effort, Toolset: agentkit.Toolset{Tools: []core.Tool{tool}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), "read a.txt")
	if err != nil || res.FinalText() != "The file says: file contents." || res.StopReason != core.RunStopEndTurn || ran != 1 || srv.Count() != 2 {
		t.Fatalf("run = %q %v, %v; tool ran %d times over %d requests", res.FinalText(), res.StopReason, err, ran, srv.Count())
	}
	for i := range 2 {
		if body := srv.body(t, i); body["model"] != sel.Model.ID || body["options"].(map[string]any)["num_ctx"] != float64(sel.Model.ContextWindow) ||
			body["think"] != string(sel.Effort) || len(body["tools"].([]any)) != 1 {
			t.Errorf("request %d = model %v, options %v, think %v, tools %v", i, body["model"], body["options"], body["think"], body["tools"])
		}
	}
	var second struct{ Messages []wireMessage }
	_ = json.Unmarshal(srv.Requests()[1].Body, &second)
	n := len(second.Messages)
	assistant, result := second.Messages[n-2], second.Messages[n-1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" || assistant.Thinking != "I should read the file" ||
		string(assistant.ToolCalls[0].Function.Arguments) != args {
		t.Errorf("assistant = %+v, want call_1 with the server's key order and the thinking", assistant)
	}
	if result.ToolCallID != "call_1" || result.ToolName != "read_file" || !strings.Contains(result.Content, "file contents") {
		t.Errorf("result = %+v", result)
	}
	if u := res.Usage; u.CostUSD != 0 || !u.Has(core.UsageCostUSD) || u.InputTokens != 198 || u.CacheReadTokens != 556 || u.OutputTokens != 170 || u.Requests != 2 {
		t.Errorf("usage = %+v, want 198 in, 556 cache read, 170 out, 2 requests and a reported cost of 0", u)
	}
}

// An agent on a server that answers 404, on a closed port, or with a stream cut
// before its done chunk ends the run with the named error and runs no tool.
func TestAFailedTurnEndsTheAgentRunWithTheNamedError(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		sentinel   error
	}{
		{"a 404", newServer(t, errorReply(404, "model not found")).URL(), ollama.ErrModelNotFound},
		{"a closed port", closedPortHost(t), ollama.ErrUnreachable},
		{"a cut stream", newServer(t, lines(chunk("partial", ""), toolChunk("call_1", "read_file", `{"a":1}`))).URL(), ollama.ErrStreamTruncated},
	} {
		p, _ := ollama.New(testModel(), ollama.Options{Host: tc.host})
		ran := 0
		tool := core.Tool{Name: "read_file", Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { ran++; return nil, nil }}
		agent, err := agentkit.New(agentkit.Config{Client: p, Model: testModel().ID, Toolset: agentkit.Toolset{Tools: []core.Tool{tool}}})
		if err != nil {
			t.Fatal(err)
		}
		if res, err := agent.Run(context.Background(), "go"); !errors.Is(err, tc.sentinel) || res.StopReason != core.RunStopError || ran != 0 {
			t.Errorf("%s: run = %v, %v, tool ran %d times; want %v and no tool run", tc.name, err, res.StopReason, ran, tc.sentinel)
		}
	}
}
