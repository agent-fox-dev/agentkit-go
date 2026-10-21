package main

import (
	"strings"
	"testing"
)

// TS-30-50 (smoke, 30-PATH-3): a user runs the command at the root of the
// repository with an explicit tier and effort. The example hands the three
// settings to agentkit.New, which selects the tier's model and builds the Ollama
// provider; the server gets the request with the effort asked for, the reply is
// printed with a summary that names what ran, and the program exits 0. Nothing
// in the path is replaced.
func TestSmokeCustomtoolsOnOllamaWithATierAndAnEffort_TS30_50(t *testing.T) {
	srv := newOllamaServer(t)
	model := tierRow(t, "SIMPLE")
	r := runExampleAt(t, "../..", "./examples/customtools", map[string]string{
		"AGENTKIT_PROVIDER": "ollama", "AGENTKIT_MODEL": "SIMPLE", "AGENTKIT_EFFORT": "high", "OLLAMA_HOST": srv.URL,
	})
	if r.exit != 0 {
		t.Fatalf("exit status %d, want 0\nstdout:\n%s\nstderr:\n%s", r.exit, r.stdout, r.stderr)
	}
	chats := srv.chats(t)
	if len(chats) != 1 || chats[0]["model"] != model || chats[0]["think"] != "high" {
		t.Errorf("the server received %v, want one POST /api/chat for %q with think \"high\"", chats, model)
	}
	for _, req := range srv.requests() {
		if req.Method != "POST" || req.Path != "/api/chat" {
			t.Errorf("the server received %s %s, want only POST /api/chat", req.Method, req.Path)
		}
	}
	if !strings.Contains(r.stdout, reply) {
		t.Errorf("the example did not print the reply %q:\n%s", reply, r.stdout)
	}
	line := summaryLine(r)
	for _, want := range []string{"ollama", model, "high"} {
		if !strings.Contains(line, want) {
			t.Errorf("the summary line %q does not name %q", line, want)
		}
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
	}
}
