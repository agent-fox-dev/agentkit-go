package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
)

// These tests run the example as a program, with go run, against a local server
// that speaks Ollama's API: no network and no credential.

// reply is the text the server answers with.
const reply = "Hello from the Ollama-shaped server."

// ollamaServer answers POST /api/chat with one text turn as NDJSON and records
// every request it gets.
type ollamaServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	Method, Path string
	Body         []byte
}

func newOllamaServer(t *testing.T) *ollamaServer {
	t.Helper()
	s := &ollamaServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.reqs = append(s.reqs, recorded{r.Method, r.URL.Path, body})
		s.mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:00Z","message":{"role":"assistant","content":%q},"done":false}`+"\n", req.Model, reply)
		fmt.Fprintf(w, `{"model":%q,"created_at":"2026-10-21T10:00:01Z","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`+"\n", req.Model)
	}))
	t.Cleanup(s.Close)
	return s
}

// requests is every request the server has received.
func (s *ollamaServer) requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.reqs...)
}

// chats are the decoded bodies of the POST /api/chat requests.
func (s *ollamaServer) chats(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range s.requests() {
		if r.Method == http.MethodPost && r.Path == "/api/chat" {
			var body map[string]any
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("a chat request is not JSON: %v", err)
			}
			out = append(out, body)
		}
	}
	return out
}

// result is what a run of the example left.
type result struct {
	stdout, stderr string
	exit           int
	// decoyHits is how many connections the decoy listener accepted.
	decoyHits int
}

// inherited are the variables of the test's own environment that a run must not
// see: they choose a provider, a model or an effort, or select a deployment or
// carry a credential of a provider this test does not use.
var inherited = []string{
	"AGENTKIT_PROVIDER", "AGENTKIT_MODEL", "AGENTKIT_EFFORT", "OLLAMA_HOST",
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL",
	"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK",
}

// runExample starts the example with go run . in a process whose environment is
// the test's own, with set added and unset removed. The Anthropic and OpenRouter
// variables are not the developer's: a key of theirs would send a real request
// if the example ever ran on the wrong provider. They point at a decoy listener
// instead, which must see no connection, so that every run also shows that on
// the Ollama path nothing but the Ollama variables is consulted.
func runExample(t *testing.T, set map[string]string, unset ...string) result {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	defer ln.Close()
	var hits atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	decoy := ln.Addr().String()

	env := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if !slices.Contains(inherited, name) {
			env[name] = value
		}
	}
	for k, v := range map[string]string{
		"ANTHROPIC_API_KEY": "decoy-key", "ANTHROPIC_BASE_URL": "http://" + decoy,
		"OPENROUTER_API_KEY": "decoy-key", "OPENROUTER_BASE_URL": "http://" + decoy + "/api/v1",
	} {
		env[k] = v
	}
	for k, v := range set {
		env[k] = v
	}
	for _, k := range unset {
		delete(env, k)
	}

	cmd := exec.Command(goBin, "run", ".")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res := result{stdout: stdout.String(), stderr: stderr.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("go run: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // a connection still on its way in
	res.decoyHits = int(hits.Load())
	return res
}

// tierRow is the catalog row the tier names on Ollama.
func tierRow(t *testing.T, tier string) string {
	t.Helper()
	sel, err := catalog.Select("ollama", tier, "")
	if err != nil {
		t.Fatal(err)
	}
	return sel.Model.ID
}

// summaryLine is the line of the program's output that starts with "[".
func summaryLine(r result) string {
	for _, l := range strings.Split(r.stdout+"\n"+r.stderr, "\n") {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			return l
		}
	}
	return ""
}

// TS-30-42 (30-REQ-9.1, 30-REQ-9.2, 30-REQ-8.4): with the provider, the tier and
// the effort set, the example sends the tier's model with the effort asked for,
// not the tier's own, prints the reply and a summary that names all three, and
// exits 0.
func TestOverridePrecedence_TS30_42(t *testing.T) {
	srv := newOllamaServer(t)
	model := tierRow(t, "SIMPLE")
	r := runExample(t, map[string]string{
		"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL, "AGENTKIT_MODEL": "SIMPLE", "AGENTKIT_EFFORT": "high",
	})
	if r.exit != 0 {
		t.Fatalf("exit status %d, want 0\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, reply) {
		t.Errorf("stdout does not carry the server's text %q:\n%s", reply, r.stdout)
	}
	line := summaryLine(r)
	for _, want := range []string{"ollama", model, "high"} {
		if !strings.Contains(line, want) {
			t.Errorf("the summary line %q does not name %q", line, want)
		}
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy for the other providers' variables accepted %d connections, want none", r.decoyHits)
	}
	chats := srv.chats(t)
	if len(chats) != 1 {
		t.Fatalf("the server received %d chat requests, want 1", len(chats))
	}
	if chats[0]["model"] != model || chats[0]["think"] != "high" {
		t.Errorf("the request names model %v and think %v, want %q and \"high\"", chats[0]["model"], chats[0]["think"], model)
	}
}

// TS-30-45 (30-REQ-9.3): an unknown provider fails before any request, with an
// error line that names it and the three known ones.
func TestUnknownProviderFailsBeforeAnyRequest_TS30_45(t *testing.T) {
	srv := newOllamaServer(t)
	r := runExample(t, map[string]string{"AGENTKIT_PROVIDER": "openai", "OLLAMA_HOST": srv.URL})
	if r.exit != 1 {
		t.Errorf("exit status %d, want 1", r.exit)
	}
	out := r.stdout + r.stderr
	for _, name := range []string{"openai", "anthropic", "openrouter", "ollama"} {
		if !strings.Contains(out, name) {
			t.Errorf("the output does not name %q:\n%s", name, out)
		}
	}
	if !strings.Contains("\n"+out, "\nerror: ") {
		t.Errorf("the output has no line starting \"error: \":\n%s", out)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the server received %d requests, want none", n)
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy for the other providers' variables accepted %d connections, want none", r.decoyHits)
	}
}

// A model id the catalog does not list is passed through as it is: every tier on
// Ollama is the same model, so only another id shows that AGENTKIT_MODEL reaches
// the request. With no effort given and none to take from a tier, none is sent.
func TestModelIDIsPassedThrough_TS30_42(t *testing.T) {
	srv := newOllamaServer(t)
	const id = "some-other-model:1b"
	r := runExample(t, map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL, "AGENTKIT_MODEL": id})
	if r.exit != 0 {
		t.Fatalf("exit status %d, want 0\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	if line := summaryLine(r); !strings.Contains(line, id) || !strings.Contains(line, "effort none") {
		t.Errorf("the summary line %q does not name the model %q and no effort", line, id)
	}
	chats := srv.chats(t)
	if len(chats) != 1 || chats[0]["model"] != id {
		t.Fatalf("the server received %v, want one chat request for the model %q", chats, id)
	}
	if think, sent := chats[0]["think"]; sent {
		t.Errorf("the request carries think %v, want none for a model with no tier and no effort", think)
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
	}
}
