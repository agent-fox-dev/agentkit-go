package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// recorded is a request a provider server received.
type recorded struct {
	Path   string
	Header http.Header
	Body   []byte
}

// providerServer is an httptest server standing in for one provider's API. It
// records every request and answers each with one canned text turn in that
// provider's wire format.
type providerServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

// newProviderServer starts a server that records a request and then calls
// reply with it. reply gets the request's JSON body, which names the model.
func newProviderServer(t *testing.T, reply func(w http.ResponseWriter, body []byte)) *providerServer {
	t.Helper()
	s := &providerServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
		s.mu.Unlock()
		reply(w, body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *providerServer) URL() string { return s.srv.URL }

// count is how many requests the server has received.
func (s *providerServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *providerServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

// modelOf is the "model" a request body names.
func modelOf(body []byte) string {
	var b struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Model
}

// anthropicServer answers the Messages API with one text turn that reports its
// usage, served by the model the request named.
func anthropicServer(t *testing.T) *providerServer {
	t.Helper()
	return newProviderServer(t, func(w http.ResponseWriter, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sseResponse(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+modelOf(body)+`","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		))
	})
}

// openRouterServer answers /chat/completions with one text turn. usage is the
// JSON of the final usage chunk ("" sends none), for example
// `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}` with or without
// a "cost".
func openRouterServer(t *testing.T, usage string) *providerServer {
	t.Helper()
	return newProviderServer(t, func(w http.ResponseWriter, body []byte) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(choices string) string {
			return `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"` + modelOf(body) +
				`","choices":` + choices
		}
		out := []string{
			chunk(`[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`),
			chunk(`[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		}
		if usage != "" {
			out = append(out, chunk(`[],"usage":`+usage+`}`))
		}
		out = append(out, "data: [DONE]")
		fmt.Fprint(w, strings.Join(out, "\n\n")+"\n\n")
	})
}

// ollamaServer answers /api/chat with one text turn as NDJSON, ending with the
// done chunk and its counts.
func ollamaServer(t *testing.T) *providerServer {
	t.Helper()
	return newProviderServer(t, func(w http.ResponseWriter, body []byte) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		m := modelOf(body)
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:00Z","message":{"role":"assistant","content":"ok"},"done":false}`+"\n", m)
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:01Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`+"\n", m)
	})
}

// streamOnce sends one request through c and returns the message it ended in.
func streamOnce(t *testing.T, c core.ProviderClient) core.AssistantMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	max := 64
	ch, err := c.Stream(ctx, core.Request{Messages: core.Messages{userText("hi")}, MaxTokens: &max})
	s := core.EventStreamOf(ch, err)
	for range s.Events() {
	}
	msg := s.Result()
	if msg == nil {
		t.Fatalf("the stream ended with no message: %v", s.Err())
	}
	return *msg
}
