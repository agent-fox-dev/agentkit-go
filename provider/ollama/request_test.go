package ollama_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// sentBody streams req on a provider for m and returns the request body.
func sentBody(t *testing.T, m core.Model, req core.Request) map[string]any {
	t.Helper()
	srv := newServer(t, okTurn())
	run(t, newClient(t, srv, m), req)
	return srv.body(t, 0)
}

// A turn is one POST to /api/chat with the model id as written, streaming, no
// truncation, num_ctx (the model's window, else the catalog default) and never
// keep_alive, format or images.
func TestAStreamPostsOneChatRequest(t *testing.T) {
	for _, tc := range []struct {
		window, numCtx int
	}{{16384, 16384}, {0, catalog.OllamaDefaultContextWindow}, {-5, catalog.OllamaDefaultContextWindow}} {
		srv := newServer(t, okTurn())
		run(t, newClient(t, srv, core.Model{ID: "Qwen3.8:27B-Odd", ContextWindow: tc.window}), userRequest("hi"))
		reqs := srv.Requests()
		if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/chat" {
			t.Fatalf("requests = %+v, want one POST to /api/chat", reqs)
		}
		body := srv.body(t, 0)
		if body["model"] != "Qwen3.8:27B-Odd" || body["stream"] != true || body["truncate"] != false {
			t.Errorf("model %v, stream %v, truncate %v", body["model"], body["stream"], body["truncate"])
		}
		if opts := body["options"].(map[string]any); opts["num_ctx"] != float64(tc.numCtx) {
			t.Errorf("window %d: options = %v, want num_ctx %d", tc.window, opts, tc.numCtx)
		}
		if b := string(reqs[0].Body); strings.Contains(b, `"keep_alive"`) || strings.Contains(b, `"format"`) || strings.Contains(b, `"images"`) {
			t.Errorf("the body carries keep_alive, format or images: %s", b)
		}
	}
}

// num_predict is the requested bound when positive and within the cap, else
// the cap, else a positive request, else left out; sampling options are sent
// only when set, a zero temperature included.
func TestModelOptionsAreSentOnlyWhenThereIsAValue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cap   int
		req   func(*core.Request)
		field string
		want  string // the option as JSON, "" for absent
	}{
		{"a bound within the cap", 32768, func(r *core.Request) { r.MaxTokens = ptr(1000) }, "num_predict", "1000"},
		{"a bound at the cap", 32768, func(r *core.Request) { r.MaxTokens = ptr(32768) }, "num_predict", "32768"},
		{"a bound above the cap", 32768, func(r *core.Request) { r.MaxTokens = ptr(50000) }, "num_predict", "32768"},
		{"no bound", 32768, func(*core.Request) {}, "num_predict", "32768"},
		{"a zero bound", 32768, func(r *core.Request) { r.MaxTokens = ptr(0) }, "num_predict", "32768"},
		{"a bound and no cap", 0, func(r *core.Request) { r.MaxTokens = ptr(1000) }, "num_predict", "1000"},
		{"no bound and no cap", 0, func(*core.Request) {}, "num_predict", ""},
		{"a zero bound and no cap", 0, func(r *core.Request) { r.MaxTokens = ptr(0) }, "num_predict", ""},
		{"temperature zero", 0, func(r *core.Request) { r.Temperature = ptr(0.0) }, "temperature", "0"},
		{"no temperature", 0, func(*core.Request) {}, "temperature", ""},
		{"top_p", 0, func(r *core.Request) { r.TopP = ptr(0.9) }, "top_p", "0.9"},
		{"no top_p", 0, func(*core.Request) {}, "top_p", ""},
		{"stop sequences", 0, func(r *core.Request) { r.StopSequences = []string{"X"} }, "stop", `["X"]`},
		{"no stop sequences", 0, func(*core.Request) {}, "stop", ""},
	} {
		req := userRequest("hi")
		tc.req(&req)
		opts := sentBody(t, core.Model{ID: "m", ContextWindow: 8192, MaxOutputTokens: tc.cap}, req)["options"].(map[string]any)
		got, has := opts[tc.field]
		if tc.want == "" && has || tc.want != "" && mustJSON(got) != tc.want {
			t.Errorf("%s: %s = %v (present %v), want %s", tc.name, tc.field, got, has, tc.want)
		}
	}
}
