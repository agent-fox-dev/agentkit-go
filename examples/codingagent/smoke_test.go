package main

import (
	"strings"
	"testing"
)

// TS-30-51 (smoke, 30-PATH-4): a user runs the command at the root of the
// repository with a provider that does not exist. agentkit.New's error names it
// and the three that do, the example prints it after "error: " and exits 1, and
// nothing is sent to any server. Nothing in the path is replaced.
func TestSmokeCodingagentWithAnUnknownProviderErrorsBeforeAnyRequest_TS30_51(t *testing.T) {
	srv := newOllamaServer(t)
	r := runExampleAt(t, "../..", "./examples/codingagent", map[string]string{"AGENTKIT_PROVIDER": "openai", "OLLAMA_HOST": srv.URL})
	if r.exit != 1 {
		t.Errorf("exit status %d, want 1", r.exit)
	}
	var errLine string
	for _, l := range strings.Split(r.stdout+"\n"+r.stderr, "\n") {
		if strings.HasPrefix(l, "error: ") {
			errLine = l
		}
	}
	if errLine == "" {
		t.Fatalf("no line starts \"error: \":\nstdout:\n%s\nstderr:\n%s", r.stdout, r.stderr)
	}
	for _, name := range []string{"openai", "anthropic", "openrouter", "ollama"} {
		if !strings.Contains(errLine, name) {
			t.Errorf("the error line %q does not name %q", errLine, name)
		}
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the server received %d requests, want none", n)
	}
	if r.decoyHits != 0 {
		t.Errorf("the decoy accepted %d connections, want none", r.decoyHits)
	}
}
