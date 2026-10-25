package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/mcpserve"
)

func TestTheHarnessCommandLinesTurnTheirOwnToolsOffAndLeaveThisServerAlone(t *testing.T) {
	o := options{MaxTurns: 7, Model: "m"}
	claude := claudeArgs(`{"mcpServers":{}}`, "sys.md", "do it", o)
	for _, want := range [][]string{{"-p", "do it"}, {"--tools", ""}, {"--strict-mcp-config"}, {"--mcp-config", `{"mcpServers":{}}`},
		{"--allowedTools", "mcp__agentkit"}, {"--permission-mode", "dontAsk"}, {"--append-system-prompt-file", "sys.md"},
		{"--max-turns", "7"}, {"--output-format", "json"}, {"--no-session-persistence"}, {"--model", "m"}} {
		if !hasSeq(claude, want) {
			t.Errorf("claude args lack %q: %q", want, claude)
		}
	}
	// The prompt comes after "--", so one that starts with a dash is still a prompt.
	pi := piArgs("sys.md", "-starts with a dash", o)
	for _, want := range [][]string{{"-p"}, {"--approve"}, {"--tools", "mcp__agentkit__*"}, {"--no-skills"},
		{"--append-system-prompt", "sys.md"}, {"--model", "m"}, {"--", "-starts with a dash"}} {
		if !hasSeq(pi, want) || pi[len(pi)-1] != "-starts with a dash" {
			t.Errorf("pi args lack %q or do not end with the prompt: %q", want, pi)
		}
	}
}

func TestAnUnknownOrMissingHarnessIsAnError(t *testing.T) {
	if _, err := runHarness(context.Background(), io.Discard, "nope", &mcpserve.Endpoint{}, "", "", options{}); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("unknown harness: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := runHarness(context.Background(), io.Discard, "claude", &mcpserve.Endpoint{}, "", "", options{}); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("a missing claude: %v", err)
	}
}

// Stand-ins record what they were given: claude its --mcp-config, pi the
// .pi/mcp.json of the directory it runs in, which the real pi reads in print
// mode only with --approve. claude prints what HARNESS_TEST_CLAUDE says and
// exits with HARNESS_TEST_EXIT.
func TestTheHarnessesAreToldOfTheEndpointAndReportWhatTheyPrinted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	bin, out := t.TempDir(), t.TempDir()
	testutil.WriteTree(t, bin, map[string]string{
		"claude": "#!/bin/sh\nprev=\nfor a in \"$@\"; do\n  if [ \"$prev\" = --mcp-config ]; then printf '%s' \"$a\" > \"$HARNESS_TEST_OUT/claude-config\"; fi\n" +
			"  prev=$a\ndone\nprintf '%s' \"$HARNESS_TEST_CLAUDE\"\nexit \"$HARNESS_TEST_EXIT\"\n",
		"pi": "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = --approve ]; then cp .pi/mcp.json \"$HARNESS_TEST_OUT/pi-config\"; fi\ndone\necho done\n",
	})
	for _, name := range []string{"claude", "pi"} {
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HARNESS_TEST_OUT", out)
	ep := &mcpserve.Endpoint{URL: "http://127.0.0.1:9/mcp", Token: strings.Repeat("a", 48)}
	for _, c := range []struct {
		printed, exit string
		want          harnessResult
	}{
		{`{"result":"done","total_cost_usd":0.5,"num_turns":3}`, "0", harnessResult{Harness: "claude", Final: "done", CostUSD: 0.5, Turns: 3}},
		{`{"is_error":true,"subtype":"error_max_turns"}`, "1", harnessResult{Harness: "claude", ExitCode: 1, Final: "claude: error_max_turns"}},
		{"  plain text\n", "0", harnessResult{Harness: "claude", Final: "plain text"}},
	} {
		t.Setenv("HARNESS_TEST_CLAUDE", c.printed)
		t.Setenv("HARNESS_TEST_EXIT", c.exit)
		if hr, err := runHarness(context.Background(), io.Discard, "claude", ep, "system", "do it", options{MaxTurns: 3}); err != nil || hr != c.want {
			t.Errorf("claude printing %q: %+v, %v; want %+v", c.printed, hr, err, c.want)
		}
	}
	if hr, err := runHarness(context.Background(), io.Discard, "pi", ep, "system", "do it", options{}); err != nil || hr != (harnessResult{Harness: "pi", Final: "done"}) {
		t.Errorf("pi: %+v, %v", hr, err)
	}
	for file, want := range map[string]string{"claude-config": ep.ClaudeConfig("agentkit"), "pi-config": ep.PiConfig("agentkit")} {
		if got, err := os.ReadFile(filepath.Join(out, file)); err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
}
