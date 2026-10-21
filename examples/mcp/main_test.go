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

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
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

// TS-30-44 (30-REQ-9.1, 30-REQ-9.2, 30-REQ-8.4): with the provider, the tier and
// the effort set, the example sends the tier's model with the effort asked for,
// not the tier's own, prints the reply and a summary that names all three, and
// exits 0.
func TestOverridePrecedence_TS30_44(t *testing.T) {
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

// TS-30-47 (30-REQ-9.3): an unknown provider fails before any request, with an
// error line that names it and the three known ones.
func TestUnknownProviderFailsBeforeAnyRequest_TS30_47(t *testing.T) {
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
func TestModelIDIsPassedThrough_TS30_44(t *testing.T) {
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

// TS-30-37 (30-REQ-8.1, 30-REQ-8.4): with only the provider chosen, the example
// runs the STANDARD tier at the tier's own effort, and says so.
func TestDefaultsAreTheStandardTier_TS30_37(t *testing.T) {
	srv := newOllamaServer(t)
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	r := runExample(t, map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL})
	if r.exit != 0 {
		t.Fatalf("exit status %d, want 0\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	chats := srv.chats(t)
	if len(chats) != 1 || chats[0]["model"] != sel.Model.ID || chats[0]["think"] != string(sel.Effort) {
		t.Fatalf("the server received %v, want one chat request for %q with think %q", chats, sel.Model.ID, sel.Effort)
	}
	line := summaryLine(r)
	for _, want := range []string{"ollama", sel.Model.ID, string(sel.Effort), "1 turns", "in 10 / out 5 tokens"} {
		if !strings.Contains(line, want) {
			t.Errorf("the summary line %q does not name %q", line, want)
		}
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
	}
}

// TS-30-39 (30-REQ-8.3): a configuration that cannot run is agentkit.New's error,
// printed as a line that starts "error: ", with exit status 1 and no request.
func TestBadConfigurationFailsBeforeAnyRequest_TS30_39(t *testing.T) {
	srv := newOllamaServer(t)
	t.Run("cases", func(t *testing.T) {
		for _, c := range []struct {
			name  string
			set   map[string]string
			unset []string
			want  string
		}{
			{"an unknown effort", map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL, "AGENTKIT_EFFORT": "bogus"}, nil, "bogus"},
			{"no OpenRouter key", map[string]string{"AGENTKIT_PROVIDER": "openrouter", "OLLAMA_HOST": srv.URL}, []string{"OPENROUTER_API_KEY"}, "OPENROUTER_API_KEY"},
			{"an unusable Ollama host", map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": "ftp://nowhere"}, nil, "ftp"},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				r := runExample(t, c.set, c.unset...)
				if r.exit != 1 {
					t.Errorf("exit status %d, want 1\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
				}
				found := false
				for _, l := range strings.Split(r.stdout+"\n"+r.stderr, "\n") {
					found = found || (strings.HasPrefix(l, "error: ") && strings.Contains(l, c.want))
				}
				if !found {
					t.Errorf("no line starts \"error: \" and names %q:\nstdout:\n%s\nstderr:\n%s", c.want, r.stdout, r.stderr)
				}
				if r.decoyHits != 0 {
					t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
				}
			})
		}
	})
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the server received %d requests, want none", n)
	}
}

// TS-30-40 (30-REQ-8.5): a run whose usage is flagged unpriced shows "cost
// unknown" and no dollar figure; a priced one shows its figure; both name the
// provider, the model and the effort.
func TestSummaryShowsCostUnknownInsteadOfAFigure_TS30_40(t *testing.T) {
	r := agentkit.Resolution{Provider: "openrouter", Model: core.Model{ID: "vendor/some-model"}, Effort: core.EffortHigh}
	run := func(u core.Usage) core.RunResult {
		return core.RunResult{TurnCount: 2, StopReason: core.RunStopEndTurn, Usage: u}
	}
	names := []string{"openrouter", "vendor/some-model", "high", "2 turns", "in 100 / out 20 tokens"}

	unpriced := summary(r, run(core.Usage{InputTokens: 100, OutputTokens: 20, Unpriced: true}))
	if !strings.Contains(unpriced, "cost unknown") || strings.Contains(unpriced, "$") {
		t.Errorf("the unpriced summary %q, want \"cost unknown\" and no dollar figure", unpriced)
	}
	// Part of a cost is still not a cost.
	partly := summary(r, run(core.Usage{InputTokens: 100, OutputTokens: 20, Unpriced: true, CostUSD: 0.5}))
	if !strings.Contains(partly, "cost unknown") || strings.Contains(partly, "$") {
		t.Errorf("the summary of a run with a cost of $0.5 that is flagged unpriced is %q, want \"cost unknown\" and no dollar figure", partly)
	}
	priced := summary(r, run(core.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.0123}))
	if !strings.Contains(priced, "$0.0123") || strings.Contains(priced, "cost unknown") {
		t.Errorf("the priced summary %q, want $0.0123 and not \"cost unknown\"", priced)
	}
	for _, s := range []string{unpriced, partly, priced} {
		for _, want := range names {
			if !strings.Contains(s, want) {
				t.Errorf("the summary %q does not name %q", s, want)
			}
		}
	}
	if s := summary(agentkit.Resolution{Provider: "ollama", Model: core.Model{ID: "m"}}, run(core.Usage{})); !strings.Contains(s, "effort none") {
		t.Errorf("the summary %q of a run with no effort, want \"effort none\"", s)
	}
}

// TS-30-48 (30-REQ-9.4): the Ollama outcome is unchanged when the developer's
// environment holds values for every other provider's variables, including the
// ones that select Vertex AI or Bedrock; none of them is consulted.
func TestForeignProviderVariablesAreNotConsulted_TS30_48(t *testing.T) {
	srv := newOllamaServer(t)
	model := tierRow(t, "SIMPLE")
	r := runExample(t, map[string]string{
		"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL, "AGENTKIT_MODEL": "SIMPLE", "AGENTKIT_EFFORT": "high",
		"ANTHROPIC_AUTH_TOKEN": "decoy", "ANTHROPIC_OAUTH_TOKEN": "decoy",
		"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_VERTEX_PROJECT_ID": "decoy", "CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "decoy",
	})
	if r.exit != 0 {
		t.Fatalf("exit status %d, want 0\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	chats := srv.chats(t)
	if len(chats) != 1 || chats[0]["model"] != model || chats[0]["think"] != "high" {
		t.Errorf("the server received %v, want one chat request for %q with think \"high\"", chats, model)
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
	}
}
