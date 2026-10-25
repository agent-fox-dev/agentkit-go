package ollama_test

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// Every failure is at most one attempt and an error turn whose error names
// what to do: its sentinel and StatusError code where it has them, the facts it
// mentions, the text and thinking streamed so far but no tool call, only the
// request counted, and no other provider's key. A refused connection keeps the
// connection error.
func TestEachFailureIsNamedAndSaysWhatToDo(t *testing.T) {
	secrets := setSecrets(t)
	m := core.Model{ID: "qwen3.8:27b", ContextWindow: 32768}
	const tooLong = "the input length exceeds the context length"
	partial := []string{chunk("", "hmm"), chunk("so far", ""), toolChunk("call_1", "f", `{"a":1}`)}
	refused := reply{status: -1} // the host is a closed port
	for _, tc := range []struct {
		name     string
		rep      reply
		sentinel error
		code     int      // the StatusError's, 0 for none
		mention  []string // "{host}" stands for the server's URL
		text     string   // the exact text, when there is one
		partial  bool     // the turn streamed the partial chunks first
	}{
		{"a closed port", refused, ollama.ErrUnreachable, 0, []string{"{host}", "ollama serve", "OLLAMA_HOST"}, "", false},
		{"a cut stream", lines(partial...), ollama.ErrStreamTruncated, 0, nil, "ollama: stream ended before done", true},
		{"a 404", errorReply(404, "model 'qwen3.8:27b' not found"), ollama.ErrModelNotFound, 404,
			[]string{"qwen3.8:27b", "{host}", "ollama pull qwen3.8:27b", "model 'qwen3.8:27b' not found"}, "", false},
		{"a 404 with no body", errorReply(404, ""), ollama.ErrModelNotFound, 404, []string{"{host}", "ollama pull qwen3.8:27b", "Not Found"}, "", false},
		{"a 401", errorReply(401, "denied"), nil, 401, []string{"{host}", "AgentKit sends no credential to Ollama", "OLLAMA_HOST"}, "", false},
		{"a 401 with no body", errorReply(401, ""), nil, 401, []string{"{host}", "OLLAMA_HOST", "Unauthorized"}, "", false},
		{"a 403", errorReply(403, "nope"), nil, 403, []string{"{host}", "AgentKit sends no credential to Ollama"}, "", false},
		{"a 429", errorReply(429, "busy"), nil, 429, nil, "ollama: HTTP 429: busy", false},
		{"a 500", errorReply(500, " boom "), nil, 500, nil, "ollama: HTTP 500: boom", false},
		{"a 503 with no body", errorReply(503, ""), nil, 503, nil, "ollama: HTTP 503: Service Unavailable", false},
		{"a 400 over the window", errorReply(400, tooLong), ollama.ErrContextWindow, 400, []string{"qwen3.8:27b", "32768", tooLong, "unloads"}, "", false},
		{"truncation over the window", errorReply(400, "input after truncation exceeds maximum context length"), ollama.ErrContextWindow, 400, nil, "", false},
		{"over the window in the stream", lines(append(partial[:2], `{"error":"input exceeds maximum context length and cannot be truncated further"}`)...),
			ollama.ErrContextWindow, 0, []string{"qwen3.8:27b", "32768", "unloads"}, "", true},
		{"an error line in the stream", lines(append(partial[:2], `{"error":"model runner crashed"}`)...), nil, 0, []string{"model runner crashed"}, "", true},
	} {
		srv, host, attempts := newServer(t, tc.rep), "", 1
		if tc.rep == refused {
			host, attempts = closedPortHost(t), 0
		}
		warn, warns := collectWarnings()
		o := run(t, newClient(t, srv, m, warn, func(o *ollama.Options) { o.Host = cmp.Or(host, o.Host) }), userRequest("hi"))
		err := failedTurn(t, o)
		if host != "" && !errors.As(err, new(*net.OpError)) {
			t.Errorf("%s: error %v does not keep the connection error", tc.name, err)
		}
		var se *ollama.StatusError
		if tc.sentinel != nil && !errors.Is(err, tc.sentinel) || errors.As(err, &se) != (tc.code != 0) || tc.code != 0 && se.Code != tc.code {
			t.Errorf("%s: error %v, want sentinel %v and StatusError code %d", tc.name, err, tc.sentinel, tc.code)
		}
		if tc.text != "" && err.Error() != tc.text {
			t.Errorf("%s: text %q, want %q", tc.name, err, tc.text)
		}
		for _, w := range tc.mention {
			if !strings.Contains(err.Error(), strings.ReplaceAll(w, "{host}", cmp.Or(host, srv.URL()))) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err, w)
			}
		}
		if want := map[bool]string{true: "so far", false: ""}[tc.partial]; o.msg.Content.Text() != want || len(core.ExtractToolUse(&o.msg)) != 0 {
			t.Errorf("%s: content %#v, want the text %q and no tool call", tc.name, o.msg.Content, want)
		}
		if u := o.msg.Usage; srv.Count() != attempts || u != (core.Usage{Requests: 1}) {
			t.Errorf("%s: %d requests, usage %+v; want %d and only the request counted", tc.name, srv.Count(), u, attempts)
		}
		for _, s := range secrets {
			if text := err.Error() + strings.Join(*warns, ""); strings.Contains(text, s) {
				t.Errorf("%s: %q holds %q", tc.name, text, s)
			}
		}
	}
}

// A reset after the server answered is not an unreachable server, and the
// text so far is kept.
func TestAResetMidStreamIsNotUnreachable(t *testing.T) {
	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, chunk("so far", ""))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.(*net.TCPConn).SetLinger(0)
			_ = conn.Close()
		}
	}))
	t.Cleanup(reset.Close)
	c, _ := ollama.New(testModel(), ollama.Options{Host: reset.URL})
	o := run(t, c, userRequest("hi"))
	if err := failedTurn(t, o); errors.Is(err, ollama.ErrUnreachable) || strings.Contains(err.Error(), "ollama serve") || o.msg.Content.Text() != "so far" {
		t.Errorf("error = %v, content %q: a reset mid-stream is not an unreachable server", err, o.msg.Content.Text())
	}
}
