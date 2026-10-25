package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// The example runs as a program, with go run, against a local server that
// speaks Ollama's API: no network and no credential.

// reply is the text the server answers with.
const reply = "Hello from the Ollama-shaped server."

// ollamaServer answers POST /api/chat with one text turn as NDJSON and keeps
// every request it gets.
type ollamaServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
	chats []map[string]any
}

func newOllamaServer(t *testing.T) *ollamaServer {
	t.Helper()
	s := &ollamaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var chat map[string]any
		_ = json.Unmarshal(body, &chat)
		s.mu.Lock()
		s.paths = append(s.paths, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost && r.URL.Path == "/api/chat" {
			s.chats = append(s.chats, chat)
		}
		s.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:00Z","message":{"role":"assistant","content":%q},"done":false}`+"\n", chat["model"], reply)
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:01Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`+"\n", chat["model"])
	}))
	t.Cleanup(s.Close)
	return s
}

// requests is how many requests the server got, and the chat requests' bodies.
func (s *ollamaServer) requests() (int, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths), append([]map[string]any(nil), s.chats...)
}

// selectionVars are the variables of the test's own environment a run must not
// inherit: they choose a provider, a model or an effort, select a deployment,
// or carry a credential of a provider the test does not use.
var selectionVars = []string{
	"AGENTKIT_PROVIDER", "AGENTKIT_MODEL", "AGENTKIT_EFFORT", "OLLAMA_HOST",
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK",
}

// runExample runs the example with go run . in the test's environment without
// selectionVars, with set added and unset removed. The Anthropic and
// OpenRouter variables point at a decoy listener, not at the developer's
// account, and decoyHits is how many connections it accepted: a run on the
// Ollama path must consult nothing but the Ollama variables.
func runExample(t *testing.T, set map[string]string, unset ...string) (res testutil.Result, decoyHits int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer ln.Close()
	decoy := "http://" + ln.Addr().String()
	env := testutil.Environ(selectionVars...)
	for k, v := range map[string]string{"ANTHROPIC_API_KEY": "decoy-key", "ANTHROPIC_BASE_URL": decoy,
		"OPENROUTER_API_KEY": "decoy-key", "OPENROUTER_BASE_URL": decoy + "/api/v1"} {
		env[k] = v
	}
	for k, v := range set {
		env[k] = v
	}
	for _, k := range unset {
		delete(env, k)
	}
	res = testutil.GoRun(t, "", ".", env)
	// A connection made while the program ran waits in the listener's backlog.
	for {
		_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Millisecond))
		c, err := ln.Accept()
		if err != nil {
			return res, decoyHits
		}
		decoyHits++
		_ = c.Close()
	}
}

// tierModel is the model the tier names on Ollama, and the tier's effort.
func tierModel(t *testing.T, tier string) (model, effort string) {
	t.Helper()
	sel, err := catalog.Select("ollama", tier, "")
	if err != nil {
		t.Fatal(err)
	}
	return sel.Model.ID, string(sel.Effort)
}

// summaryLine is the line of the program's output that is wrapped in brackets.
func summaryLine(r testutil.Result) string {
	for _, l := range strings.Split(r.Stdout+"\n"+r.Stderr, "\n") {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			return l
		}
	}
	return ""
}
