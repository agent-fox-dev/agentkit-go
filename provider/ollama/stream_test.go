package ollama_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// sentBody decodes the JSON body of the n-th request the server received.
func sentBody(t *testing.T, srv *recorder, n int) map[string]any {
	t.Helper()
	reqs := srv.Requests()
	if n >= len(reqs) {
		t.Fatalf("the server holds %d requests, want at least %d", len(reqs), n+1)
	}
	var body map[string]any
	if err := json.Unmarshal(reqs[n].Body, &body); err != nil {
		t.Fatalf("request %d body is not JSON: %v\n%s", n, err, reqs[n].Body)
	}
	return body
}

// roundTripFunc is an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// writeSigningKey writes an Ed25519 key where Ollama's SDK looks for the one it
// signs requests with, under home.
func writeSigningKey(t *testing.T, home string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".ollama")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), pemKey, 0o600); err != nil {
		t.Fatal(err)
	}
}

// onePlainTurn is a server that answers one text turn.
func onePlainTurn(t *testing.T) *recorder {
	t.Helper()
	return newRecorder(t, lines(chunk("ok", ""), doneChunk("served-model", "stop")))
}

// TS-28-9 (integration): no Authorization header or foreign provider value
// reaches the server, whatever the SDK adds; a header the embedder's own
// transport adds does, because that transport runs beneath the layer that
// deletes Authorization (28-REQ-1.8, Design Decision 6).
func TestNoCredentialIsSent_TS28_9(t *testing.T) {
	secrets := []string{"sk-ant-secret-1a2b", "sk-or-secret-3c4d", "ollama-secret-5e6f"}
	for i, name := range []string{"ANTHROPIC_API_KEY", "OPENROUTER_API_KEY", "OLLAMA_API_KEY"} {
		t.Setenv(name, secrets[i])
	}

	t.Run("API keys in the environment", func(t *testing.T) {
		srv := onePlainTurn(t)
		if o := run(t, newClient(t, srv, testModel()), userRequest("hi")); o.err != nil {
			t.Fatal(o.err)
		}
		checkCredentialFree(t, srv, secrets)
	})

	t.Run("the SDK's own signing path", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the SDK finds its key through $HOME")
		}
		home := t.TempDir()
		writeSigningKey(t, home)
		t.Setenv("HOME", home)
		t.Setenv("OLLAMA_AUTH", "1")
		srv := onePlainTurn(t)
		if o := run(t, newClient(t, srv, testModel()), userRequest("hi")); o.err != nil {
			t.Fatal(o.err)
		}
		// The SDK signs the request, which adds a ts parameter as well as the
		// header; without it the test would check nothing.
		if q := srv.Requests()[0].RawQuery; !strings.HasPrefix(q, "ts=") {
			t.Fatalf("the query is %q: the SDK did not take its signing path", q)
		}
		checkCredentialFree(t, srv, secrets)
	})

	t.Run("a header the embedder's transport adds", func(t *testing.T) {
		srv := onePlainTurn(t)
		c := newClient(t, srv, testModel(), func(o *ollama.Options) {
			o.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				r = r.Clone(r.Context())
				r.Header.Set("X-Embedder", "yes")
				r.Header.Set("Authorization", "Bearer proxy-token")
				return http.DefaultTransport.RoundTrip(r)
			})}
		})
		if o := run(t, c, userRequest("hi")); o.err != nil {
			t.Fatal(o.err)
		}
		h := srv.Requests()[0].Header
		if h.Get("X-Embedder") != "yes" || h.Get("Authorization") != "Bearer proxy-token" {
			t.Errorf("headers %v: the embedder's X-Embedder and Authorization should reach the server", h)
		}
	})
}

// checkCredentialFree fails the test if a request the server received carries an
// Authorization header, or a secret anywhere in its URL, headers or body.
func checkCredentialFree(t *testing.T, srv *recorder, secrets []string) {
	t.Helper()
	if srv.Count() == 0 {
		t.Fatal("the server received no request")
	}
	for i, r := range srv.Requests() {
		if v := r.Header.Get("Authorization"); v != "" {
			t.Errorf("request %d carries Authorization %q", i, v)
		}
		all := r.Path + "?" + r.RawQuery + "\n" + string(r.Body)
		for k, vs := range r.Header {
			all += "\n" + k + ": " + strings.Join(vs, ",")
		}
		for _, s := range secrets {
			if strings.Contains(all, s) {
				t.Errorf("request %d carries %q", i, s)
			}
		}
	}
}

// TS-28-10 (integration): a Stream call posts one /api/chat request with the
// model as written, stream, truncate false and num_ctx.
func TestStreamPostsOneChatRequest_TS28_10(t *testing.T) {
	srv := onePlainTurn(t)
	m := core.Model{ID: "Qwen3.8:27B-Odd", ContextWindow: 16384}
	run(t, newClient(t, srv, m), userRequest("hi"))

	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/chat" {
		t.Fatalf("requests = %+v, want one POST to /api/chat", reqs)
	}
	body := sentBody(t, srv, 0)
	opts, _ := body["options"].(map[string]any)
	if body["model"] != "Qwen3.8:27B-Odd" || body["stream"] != true {
		t.Errorf("model %v, stream %v; want the id as written and true", body["model"], body["stream"])
	}
	if v, ok := body["truncate"]; !ok || v != false {
		t.Errorf("truncate = %v (present %v), want false", v, ok)
	}
	if opts["num_ctx"] != float64(16384) {
		t.Errorf("options = %v, want num_ctx 16384", opts)
	}
}

// TS-28-11: a model with no positive context window sends the catalog default
// as num_ctx.
func TestNoWindowSendsTheCatalogDefault_TS28_11(t *testing.T) {
	for _, window := range []int{0, -5} {
		srv := onePlainTurn(t)
		run(t, newClient(t, srv, core.Model{ID: "m", ContextWindow: window}), userRequest("hi"))
		opts, _ := sentBody(t, srv, 0)["options"].(map[string]any)
		if opts["num_ctx"] != float64(catalog.OllamaDefaultContextWindow) {
			t.Errorf("window %d: options = %v, want num_ctx %d", window, opts, catalog.OllamaDefaultContextWindow)
		}
	}
}

// TS-28-26: the first event is a MessageStartEvent with the model, the effort
// and the clock's time.
func TestFirstEventIsMessageStart_TS28_26(t *testing.T) {
	srv := onePlainTurn(t)
	req := userRequest("hi")
	req.Effort = core.EffortMedium
	o := run(t, newClient(t, srv, testModel()), req)

	start, ok := o.events[0].(core.MessageStartEvent)
	if !ok {
		t.Fatalf("the first event is %T, want a MessageStartEvent", o.events[0])
	}
	if got := start.Message; got.Model != "test-model:1b" || got.Effort != core.EffortMedium || !got.Timestamp.Equal(fixedNow) {
		t.Errorf("start message = {Model %q Effort %q Timestamp %v}, want the model, medium and %v",
			got.Model, got.Effort, got.Timestamp, fixedNow)
	}
}

// TS-28-27: thinking and content stream as separate block kinds, each closing
// when the kind switches, and thinking is never reported as text.
func TestThinkingAndContentAreSeparateBlocks_TS28_27(t *testing.T) {
	srv := newRecorder(t, lines(
		chunk("", "pon"), chunk("", "der"), chunk("ans", ""), chunk("wer", ""),
		doneChunk("served-model", "stop")))
	o := run(t, newClient(t, srv, testModel()), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}

	want := []string{"start", "think+0", "think~0", "think~0", "update", "text+1", "text~1", "text~1", "update", "think-0", "text-1", "end"}
	if got := kinds(o.events); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v\nwant     %v", got, want)
	}
	wantContent := core.Content{core.ThinkingBlock{Thinking: "ponder"}, core.TextBlock{Text: "answer"}}
	if !reflect.DeepEqual(o.msg.Content, wantContent) {
		t.Errorf("content = %#v, want %#v", o.msg.Content, wantContent)
	}
	var updates []core.MessageUpdateEvent
	for _, e := range o.events {
		switch v := e.(type) {
		case core.MessageUpdateEvent:
			updates = append(updates, v)
		case core.TextDeltaEvent:
			if strings.Contains(v.Delta, "pon") || strings.Contains(v.Delta, "der") {
				t.Errorf("a text delta carries thinking: %q", v.Delta)
			}
		}
	}
	if len(updates) != 2 || len(updates[0].Message.Content) != 1 || len(updates[1].Message.Content) != 2 {
		t.Errorf("the updates hold %v blocks, want 1 then 2", updates)
	}
}

// turnOf streams one turn whose chunks are given, with the done chunk naming the
// served model "resp-model" and the reason.
func turnOf(t *testing.T, reason string, chunks ...string) outcome {
	t.Helper()
	srv := newRecorder(t, lines(append(chunks, doneChunk("resp-model", reason))...))
	return run(t, newClient(t, srv, testModel()), userRequest("hi"))
}

// TS-28-30: the done chunk closes the blocks in order and builds the final
// message with the raw stop reason, the response model and the stop mapping.
func TestDoneChunkBuildsTheFinalMessage_TS28_30(t *testing.T) {
	tool := toolChunk("call_1", "lookup", `{"q":1}`)
	cases := []struct {
		name       string
		reason     string
		chunks     []string
		wantStop   core.StopReason
		wantEvents []string
	}{
		{"stop with a tool call", "stop", []string{chunk("t", ""), tool}, core.StopReasonToolUse,
			[]string{"start", "text+0", "text~0", "update", "tool+1", "tool~1", "update", "text-0", "tool-1", "end"}},
		{"length with a tool call", "length", []string{chunk("t", ""), tool}, core.StopReasonLength,
			[]string{"start", "text+0", "text~0", "update", "tool+1", "tool~1", "update", "text-0", "tool-1", "end"}},
		{"text only", "stop", []string{chunk("t", "")}, core.StopReasonStop,
			[]string{"start", "text+0", "text~0", "update", "text-0", "end"}},
		{"thinking, text and a tool call", "stop", []string{chunk("", "p"), chunk("t", ""), tool}, core.StopReasonToolUse,
			[]string{"start", "think+0", "think~0", "update", "text+1", "text~1", "update", "tool+2", "tool~2", "update", "think-0", "text-1", "tool-2", "end"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := turnOf(t, tc.reason, tc.chunks...)
			if o.err != nil {
				t.Fatal(o.err)
			}
			if got := kinds(o.events); !reflect.DeepEqual(got, tc.wantEvents) {
				t.Errorf("events = %v\nwant     %v", got, tc.wantEvents)
			}
			if o.msg.StopReason != tc.wantStop || o.msg.RawStopReason != tc.reason || o.msg.ResponseModel != "resp-model" {
				t.Errorf("message = {%q raw %q model %q}, want {%q raw %q model resp-model}",
					o.msg.StopReason, o.msg.RawStopReason, o.msg.ResponseModel, tc.wantStop, tc.reason)
			}
		})
	}
}

// An embedder's http.Client keeps its settings: the provider sends through a
// copy of it, so a Timeout still cuts a request that does not answer, and a
// client with none is cut only by the caller's context.
func TestEmbedderClientSettingsAreKept(t *testing.T) {
	srv := newRecorder(t, reply{status: http.StatusOK, hold: true})
	c := newClient(t, srv, testModel(), func(o *ollama.Options) {
		o.HTTPClient = &http.Client{Timeout: 150 * time.Millisecond}
	})
	start := time.Now()
	o := run(t, c, userRequest("hi"))
	if o.err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err = %v after %v, want the client's 150ms timeout to end the turn", o.err, time.Since(start))
	}
}
