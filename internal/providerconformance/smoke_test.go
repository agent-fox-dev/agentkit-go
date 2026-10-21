package providerconformance

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-30-49 (smoke, 30-PATH-1): a developer runs one OpenRouter case. go test
// selects exactly that subtest, and the case, run with nothing replaced, goes
// from the provider's name through catalog.Select, provider/openrouter and the
// fake's wire to the loop and back: two requests to the Chat Completions path,
// echo run once, the call and its result replayed, the skeleton faux's.
func TestSmokeOpenRouterToolCallThroughResolutionProviderFakeAndLoop_TS30_49(t *testing.T) {
	// The pattern ends in $: without it, tool_call also selects tool_call_stop.
	events := goTest(t, nil, "-short", "-v", "-run", `TestProviderConformance.*/openrouter/tool_call$`, ".")
	if got, want := conformanceSubtests(events, "pass"), []string{"TestProviderConformance/openrouter/tool_call"}; !slices.Equal(got, want) {
		t.Errorf("go test -run selected and passed %v, want exactly %v", got, want)
	}

	p := providerByName("openrouter")
	rec := &recorder{}
	r := runCase(rec, p, toolCallCase, runOptions{})
	if rec.failed() {
		t.Fatalf("the case failed: %v", rec.all())
	}

	sel, err := catalog.Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	res := r.agent.Resolution()
	if res.Provider != "openrouter" || res.Tier != catalog.TierStandard || res.Model.ID != sel.Model.ID || res.Effort != sel.Effort || !res.Listed {
		t.Errorf("agentkit.New resolved %+v, want the listed STANDARD row of openrouter at %q", res, sel.Effort)
	}
	if client := fmt.Sprintf("%T", res.Client); !strings.Contains(client, "openrouter.") {
		t.Errorf("agentkit.New built the client %s, want the openrouter provider's", client)
	}

	if len(r.requests) != 2 {
		t.Fatalf("the fake recorded %d requests, want 2", len(r.requests))
	}
	for i, req := range r.requests {
		if req.Method != "POST" || req.Path != "/api/v1/chat/completions" {
			t.Errorf("request %d was %s %s, want POST /api/v1/chat/completions", i+1, req.Method, req.Path)
		}
	}
	if n := r.echoCalls.Load(); n != 1 {
		t.Errorf("echo ran %d times, want once", n)
	}
	second := r.summaries[1]
	if len(second.Assistants) != 1 || len(second.Assistants[0].ToolCalls) != 1 || second.Assistants[0].ToolCalls[0].ID != toolCallID ||
		len(second.Results) != 1 || second.Results[0].CallID != toolCallID {
		t.Errorf("the second request carries %+v, want the call and its result", second)
	}
	want, err := fauxSkeleton(r.script[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := skeleton(providerEvents(r.events, 0)); !slices.Equal(got, want) {
		t.Errorf("the first turn's skeleton is\n  %v\nwant provider/faux's\n  %v", got, want)
	}
	if r.res.StopReason != core.RunStopEndTurn || r.runErr != nil || r.res.TurnCount != 2 || r.res.FinalText() != "All done." {
		t.Errorf("the run ended %q with %v after %d turns and %q, want %q, no error, 2 turns and \"All done.\"",
			r.res.StopReason, r.runErr, r.res.TurnCount, r.res.FinalText(), core.RunStopEndTurn)
	}
}

// makeTarget runs make provider-live-test at the repository root with environ and
// returns what it printed. The tests that use it skip themselves in a go test
// run that another test started, and where there is no make.
func makeTarget(t *testing.T, environ []string) string {
	t.Helper()
	if os.Getenv(childEnv) != "" {
		t.Skip("this is a go test run started by another test of the package")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	cmd := exec.Command("make", "provider-live-test")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(environ, childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make provider-live-test: %v\n%s", err, out)
	}
	return string(out)
}

// runOutput is what go test -v printed for TestLive/<provider> between its
// "=== RUN" line and the next line of the report: the skip message, or the log of
// a run.
func runOutput(out, provider string) string {
	_, rest, ok := strings.Cut(out, "=== RUN   TestLive/"+provider+"\n")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "\n=== RUN"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.Index(rest, "\n--- "); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// TS-30-34 (smoke, 30-PATH-2), where no provider is ready: the Makefile target
// runs TestLive, which checks each provider's prerequisites and skips them all,
// each naming what is missing, and the target ends with exit status 0. The
// environment holds no key, no deployment and an Ollama host nothing listens on.
func TestSmokeMakeTargetSkipsCleanlyWhenNothingIsReady_TS30_34(t *testing.T) {
	out := makeTarget(t, hermeticEnv("OLLAMA_HOST=127.0.0.1:1"))
	if !strings.Contains(out, "AGENTKIT_LIVE=1 go test") {
		t.Errorf("the target did not run the live command:\n%s", out)
	}
	for _, c := range []struct{ provider, missing string }{
		{"anthropic", "anthropic: missing credentials"},
		{"openrouter", "OPENROUTER_API_KEY"},
		{"ollama", "ollama serve"},
	} {
		if !strings.Contains(out, "--- SKIP: TestLive/"+c.provider) {
			t.Errorf("TestLive/%s did not skip:\n%s", c.provider, out)
		}
		if said := runOutput(out, c.provider); !strings.Contains(said, c.missing) {
			t.Errorf("the skip of %s says %q, want it to say what is missing (%q)", c.provider, said, c.missing)
		}
	}
	if strings.Contains(out, "--- FAIL") || strings.Contains(out, "--- PASS: TestLive/") {
		t.Errorf("with nothing ready, no provider may run or fail:\n%s", out)
	}
}

// TS-30-34 (smoke, 30-PATH-2), against the live services: make provider-live-test
// in the environment as it is, which has to hold at least one provider's
// prerequisites for anything to run. It costs money on Anthropic and OpenRouter,
// so it needs the same opt-in as the tests it runs, and nothing else starts it:
// make check, make check-release and make provider-live-test itself do not.
func TestSmokeMakeTargetAgainstTheLiveServices_TS30_34(t *testing.T) {
	if testing.Short() {
		t.Skip("this runs the live provider tests and is skipped under -short; " + liveHint)
	}
	if os.Getenv("AGENTKIT_LIVE") != "1" {
		t.Skip("AGENTKIT_LIVE is not 1: this spends money and runs models; " + liveHint)
	}
	out := makeTarget(t, os.Environ())
	passed := 0
	for _, p := range providerTable {
		switch {
		case strings.Contains(out, "--- PASS: TestLive/"+p.name):
			passed++
			t.Logf("%s ran its tool loop", p.name)
		case strings.Contains(out, "--- SKIP: TestLive/"+p.name):
			t.Logf("%s skipped", p.name)
		default:
			t.Errorf("TestLive/%s neither passed nor skipped:\n%s", p.name, out)
		}
	}
	if passed == 0 {
		t.Log("no provider was ready: every subtest skipped, naming what is missing")
	}
	fmt.Fprintln(os.Stderr, "live smoke:", passed, "of", len(providerTable), "providers ran")
}
