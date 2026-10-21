package ollama_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-28-31: concurrent Stream calls on one Client complete independently, with
// no data race (run it under -race). The request carries nothing that differs
// from one call to the next yet, so the server hands out the numbers in the order
// requests arrive. Each response carries thinking, text sent in three chunks with
// a pause between them, which makes the streams overlap, and a tool call with no
// id, all of one number; a stream that shared state with another would come back
// with pieces of two, or with an id another stream also got.
func TestConcurrentStreamsAreIndependent_TS28_31(t *testing.T) {
	var arrived atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := arrived.Add(1) - 1
		marker := fmt.Sprintf("marker-%02d", n)
		send := func(line string) {
			fmt.Fprintln(w, line)
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
		send(chunk("", fmt.Sprintf("think-%02d", n)))
		for _, piece := range []string{marker[:3], marker[3:7], marker[7:]} {
			send(chunk(piece, ""))
		}
		send(toolChunk("", "lookup", fmt.Sprintf(`{"n":%d}`, n)))
		send(doneChunk("served-model", "stop"))
	}))
	t.Cleanup(srv.Close)
	c := newClient(t, &recorder{srv: srv}, testModel())

	const streams = 32
	msgs := make([]core.AssistantMessage, streams)
	errs := make([]error, streams)
	var wg sync.WaitGroup
	for i := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, err := c.Stream(context.Background(), userRequest("hi"))
			if err != nil {
				errs[i] = err
				return
			}
			o := drain(ch)
			msgs[i], errs[i] = o.msg, o.err
		}()
	}
	wg.Wait()

	numbers, ids := map[string]bool{}, map[string]bool{}
	for i, m := range msgs {
		if errs[i] != nil {
			t.Fatalf("stream %d: %v", i, errs[i])
		}
		tools := core.ExtractToolUse(&m)
		if len(m.Content) != 3 || len(tools) != 1 {
			t.Fatalf("stream %d: content %#v, want thinking, text and one tool call", i, m.Content)
		}
		n := m.Content.Text()[len("marker-"):]
		thinking, _ := m.Content[0].(core.ThinkingBlock)
		if thinking.Thinking != "think-"+n || string(tools[0].Input) != fmt.Sprintf(`{"n":%d}`, mustAtoi(t, n)) {
			t.Errorf("stream %d: thinking %q, text %q, input %s: the parts are from different responses", i, thinking.Thinking, m.Content.Text(), tools[0].Input)
		}
		if numbers[n] || ids[tools[0].ID] || tools[0].ID == "" {
			t.Errorf("stream %d: number %s or tool id %q is also another stream's", i, n, tools[0].ID)
		}
		numbers[n], ids[tools[0].ID] = true, true
	}
	if len(numbers) != streams || len(ids) != streams {
		t.Errorf("%d numbers and %d ids over %d streams, want one of each per stream", len(numbers), len(ids), streams)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("%q is not a number: %v", s, err)
	}
	return n
}
