package providerconformance

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// The tests in this file are about the live tests, and none is named TestLive...,
// so that make provider-live-test, which runs -run TestLive, runs only TestLive.

// providerEnvPrefixes are the variables a child of these tests must not inherit:
// a key or a server of the developer's would turn a skip into a real run.
var providerEnvPrefixes = []string{
	"ANTHROPIC_", "CLAUDE_CODE_", "OPENROUTER_", "OLLAMA_", "AGENTKIT_",
	"AWS_", "GOOGLE_", "CLOUDSDK_", "CLOUD_ML_",
}

// hermeticEnv is the process environment without any variable a provider reads,
// then extra.
func hermeticEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(providerEnvPrefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// output is everything the events of one test and its subtests printed.
func output(events []testEvent, test string) string {
	var b strings.Builder
	for _, e := range events {
		if e.Output != "" && (e.Test == test || strings.HasPrefix(e.Test, test+"/")) {
			b.WriteString(e.Output)
		}
	}
	return b.String()
}

// action is the last action recorded for test.
func action(events []testEvent, test string) string {
	a := ""
	for _, e := range events {
		if e.Test == test && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			a = e.Action
		}
	}
	return a
}

// recordingServer is a loopback server that counts and records its requests and
// answers each with answer.
type recordingServer struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newRecordingServer(t *testing.T, answer http.HandlerFunc) *recordingServer {
	t.Helper()
	s := &recordingServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.seen = append(s.seen, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *recordingServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// tagsAnswer answers /api/tags with the models and anything else with 404.
func tagsAnswer(models ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		var list []string
		for _, m := range models {
			list = append(list, fmt.Sprintf(`{"name":%q,"model":%q}`, m, m))
		}
		fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(list, ","))
	}
}

// TS-30-27 (30-REQ-6.1): TestLive skips under -short before it contacts any
// service, even with AGENTKIT_LIVE=1 and every provider pointed at a server.
func TestGateSkipsUnderShortBeforeAnyRequest_TS30_27(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, tagsAnswer())
	events := goTestIn(t, hermeticEnv("AGENTKIT_LIVE=1", "OPENROUTER_API_KEY=decoy-key",
		"OPENROUTER_BASE_URL="+srv.URL+"/api/v1", "OLLAMA_HOST="+srv.URL), "-short", "-v", "-run", "TestLive", ".")
	if got := action(events, "TestLive"); got != "skip" {
		t.Errorf("TestLive ended %q under -short, want skip", got)
	}
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the server saw %d requests %v, want none", n, srv.requests())
	}
}

// TS-30-28 (30-REQ-6.2): without -short, TestLive skips unless AGENTKIT_LIVE is
// exactly 1, naming the variable and the make target.
func TestGateSkipsUnlessLiveIsOne_TS30_28(t *testing.T) {
	t.Parallel()
	srv := newRecordingServer(t, tagsAnswer())
	t.Run("values", func(t *testing.T) {
		for _, c := range []struct {
			name string
			env  []string
		}{
			{"unset", nil},
			{"0", []string{"AGENTKIT_LIVE=0"}},
			{"true", []string{"AGENTKIT_LIVE=true"}},
			{"empty", []string{"AGENTKIT_LIVE="}},
			{"1 with a space", []string{"AGENTKIT_LIVE=1 "}},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				env := append(hermeticEnv("OPENROUTER_API_KEY=decoy-key", "OPENROUTER_BASE_URL="+srv.URL+"/api/v1", "OLLAMA_HOST="+srv.URL), c.env...)
				events := goTestIn(t, env, "-v", "-run", "TestLive", ".")
				out := output(events, "TestLive")
				if got := action(events, "TestLive"); got != "skip" {
					t.Errorf("TestLive ended %q, want skip", got)
				}
				if !strings.Contains(out, "AGENTKIT_LIVE") || !strings.Contains(out, "make provider-live-test") {
					t.Errorf("the skip message does not name AGENTKIT_LIVE and make provider-live-test:\n%s", out)
				}
			})
		}
	})
	if n := len(srv.requests()); n != 0 {
		t.Errorf("the server saw %d requests %v, want none", n, srv.requests())
	}
}

// live runs TestLive/<provider> in a child with AGENTKIT_LIVE=1, the provider
// variables of a developer who has none, and extra, and returns its events.
func live(t *testing.T, provider string, extra ...string) []testEvent {
	t.Helper()
	return goTestIn(t, hermeticEnv(append([]string{"AGENTKIT_LIVE=1"}, extra...)...), "-v", "-run", "TestLive/"+provider+"$", ".")
}

// TS-30-29 (30-REQ-6.3): the anthropic subtest skips with the error
// anthropic.Resolve gives for an environment with no credential.
func TestAnthropicPrerequisiteIsAResolvableDeployment_TS30_29(t *testing.T) {
	t.Parallel()
	_, _, want := anthropic.Resolve(anthropic.MapEnv{})
	if want == nil {
		t.Fatal("anthropic.Resolve succeeded for an empty environment")
	}
	events := live(t, "anthropic")
	if got := action(events, "TestLive/anthropic"); got != "skip" {
		t.Errorf("TestLive/anthropic ended %q, want skip", got)
	}
	if out := output(events, "TestLive/anthropic"); !strings.Contains(out, want.Error()) {
		t.Errorf("the skip message does not hold Resolve's error %q:\n%s", want, out)
	}
}

// TS-30-30 (30-REQ-6.4): the openrouter subtest skips naming OPENROUTER_API_KEY
// when it is empty or unset.
func TestOpenRouterPrerequisiteIsTheKey_TS30_30(t *testing.T) {
	t.Parallel()
	for _, extra := range [][]string{nil, {"OPENROUTER_API_KEY="}} {
		events := live(t, "openrouter", extra...)
		if got := action(events, "TestLive/openrouter"); got != "skip" {
			t.Errorf("TestLive/openrouter ended %q with %v, want skip", got, extra)
		}
		if out := output(events, "TestLive/openrouter"); !strings.Contains(out, "OPENROUTER_API_KEY") {
			t.Errorf("the skip message with %v does not name OPENROUTER_API_KEY:\n%s", extra, out)
		}
	}
}

// ollamaRow is the model of the STANDARD tier on Ollama.
func ollamaRow(t *testing.T) core.Model {
	t.Helper()
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	return sel.Model
}

// TS-30-31 (30-REQ-6.5): the ollama subtest skips when no host can be made of
// OLLAMA_HOST, naming the error; when the server is unreachable, when it answers
// /api/tags with a status that is not 200, and when it takes longer than two
// seconds, naming the host and ollama serve.
func TestOllamaPrerequisiteIsAServerAtTheHost_TS30_31(t *testing.T) {
	t.Parallel()
	row := ollamaRow(t)

	t.Run("no host", func(t *testing.T) {
		t.Parallel()
		_, want := ollama.New(row, ollama.Options{Host: "ftp://nowhere"})
		if want == nil {
			t.Fatal("ollama.New accepted an ftp host")
		}
		events := live(t, "ollama", "OLLAMA_HOST=ftp://nowhere")
		if got := action(events, "TestLive/ollama"); got != "skip" {
			t.Errorf("ended %q, want skip", got)
		}
		if out := output(events, "TestLive/ollama"); !strings.Contains(out, want.Error()) {
			t.Errorf("the skip message does not hold ollama.New's error %q:\n%s", want, out)
		}
	})

	t.Run("nothing listens", func(t *testing.T) {
		t.Parallel()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Skip(err)
		}
		host := ln.Addr().String()
		ln.Close() // the port is now closed
		assertOllamaSkip(t, live(t, "ollama", "OLLAMA_HOST="+host), host)
	})

	t.Run("status 500", func(t *testing.T) {
		t.Parallel()
		srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "boom", http.StatusInternalServerError) })
		assertOllamaSkip(t, live(t, "ollama", "OLLAMA_HOST="+srv.URL), strings.TrimPrefix(srv.URL, "http://"))
		if got := srv.requests(); !slices.Equal(got, []string{"GET /api/tags"}) {
			t.Errorf("the server saw %v, want one GET /api/tags", got)
		}
	})

	t.Run("status 503 with a list that holds the model", func(t *testing.T) {
		t.Parallel()
		srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			tagsAnswer(row.ID)(w, r)
		})
		assertOllamaSkip(t, live(t, "ollama", "OLLAMA_HOST="+srv.URL), strings.TrimPrefix(srv.URL, "http://"))
	})

	t.Run("two seconds", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
		})
		defer close(release)
		events := live(t, "ollama", "OLLAMA_HOST="+srv.URL)
		assertOllamaSkip(t, events, strings.TrimPrefix(srv.URL, "http://"))
		// The request is given up after two seconds, not at once and not never.
		for _, e := range events {
			if e.Test == "TestLive/ollama" && e.Action == "skip" && (e.Elapsed < 1.9 || e.Elapsed > 6) {
				t.Errorf("the subtest skipped after %.2fs, want the 2-second timeout of the tags request", e.Elapsed)
			}
		}
	})
}

// assertOllamaSkip requires the ollama subtest to have skipped naming the host
// and ollama serve.
func assertOllamaSkip(t *testing.T, events []testEvent, host string) {
	t.Helper()
	if got := action(events, "TestLive/ollama"); got != "skip" {
		t.Errorf("TestLive/ollama ended %q, want skip", got)
	}
	out := output(events, "TestLive/ollama")
	if !strings.Contains(out, host) || !strings.Contains(out, "ollama serve") {
		t.Errorf("the skip message does not name the host %s and ollama serve:\n%s", host, out)
	}
}

// TS-30-32 (30-REQ-6.6): the ollama subtest skips naming ollama pull and the
// model when the server does not list the STANDARD tier's model, and sends no
// chat request.
func TestOllamaPrerequisiteIsTheTierModel_TS30_32(t *testing.T) {
	t.Parallel()
	row := ollamaRow(t)
	srv := newRecordingServer(t, tagsAnswer("other:1b"))
	events := live(t, "ollama", "OLLAMA_HOST="+srv.URL)
	if got := action(events, "TestLive/ollama"); got != "skip" {
		t.Errorf("TestLive/ollama ended %q, want skip", got)
	}
	if out := output(events, "TestLive/ollama"); !strings.Contains(out, "ollama pull "+row.ID) {
		t.Errorf("the skip message does not name \"ollama pull %s\":\n%s", row.ID, out)
	}
	for _, r := range srv.requests() {
		if strings.HasPrefix(r, "POST ") {
			t.Errorf("the server saw %s: nothing may be sent to a server without the model", r)
		}
	}
	if got := srv.requests(); !slices.Equal(got, []string{"GET /api/tags"}) {
		t.Errorf("the server saw %v, want one GET /api/tags", got)
	}
}

// TS-30-35 (30-REQ-7.3, 30-REQ-7.4) and the run of 30-REQ-7.1 and 30-REQ-7.2, on
// the three fakes: the live run, as TestLive makes it, against a provider that
// answers a read_file call and then the word. The agent is the one TestLive
// builds but for a Getenv that points it at the fake.
func TestRunAgainstTheFakes_TS30_35(t *testing.T) {
	const word = "tamarind-7203"
	for _, p := range providerTable {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			fk := newFake(t, p.vendor,
				logicalTurn{blocks: []block{toolBlock("call_live_1", "read_file", `{"path":"secret.txt"}`)}, end: endToolStop},
				logicalTurn{blocks: []block{textBlock("The word is " + word + ".")}, end: endNormal})
			defer fk.Close()
			env := fakeEnv(p, fk.URL())
			runLive(t, p, liveOptions{word: word, configure: func(cfg *agentkit.Config) {
				cfg.Getenv = env.Getenv
				// What TestLive sets, still set.
				if cfg.MaxTurns != 6 || cfg.MaxCostUSD != 0.50 || cfg.Timeout != 10*time.Minute || cfg.Model != "STANDARD" || cfg.Provider != p.name ||
					len(cfg.Toolset.Tools) != 1 || cfg.Toolset.Tools[0].Name != "read_file" {
					t.Errorf("the live Config is %+v, want Provider %s, Model STANDARD, read_file alone, 6 turns, $0.50 and 10 minutes", cfg, p.name)
				}
			}})
			sums, err := fk.Summaries()
			if err != nil || len(sums) != 2 {
				t.Fatalf("%d requests decoded (%v), want 2", len(sums), err)
			}
			if !slices.Equal(sums[0].Tools, []string{"read_file"}) {
				t.Errorf("the run declared the tools %v, want read_file alone", sums[0].Tools)
			}
			if len(sums[1].Results) != 1 || !strings.Contains(sums[1].Results[0].Text, word) {
				t.Errorf("the second request carries the results %+v, want the file's content with %q", sums[1].Results, word)
			}
		})
	}
}

// goodLiveRun is the run of a provider that passes assertLiveRun.
func goodLiveRun(t *testing.T, p providerInfo) liveRun {
	t.Helper()
	sel, err := catalog.Select(p.name, "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	cost := 0.01
	if p.name == "ollama" {
		cost = 0
	}
	return liveRun{
		word:       "tamarind-7203",
		resolution: agentkit.Resolution{Provider: p.name, Tier: catalog.TierStandard, Model: sel.Model, Effort: sel.Effort},
		res: core.RunResult{
			StopReason: core.RunStopEndTurn, TurnCount: 2,
			Usage: core.Usage{InputTokens: 10, OutputTokens: 5, CostUSD: cost},
			Messages: core.Messages{
				core.UserMessage{}, core.AssistantMessage{},
				core.ToolResultMessage{ToolName: "read_file"},
				core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "The word is tamarind-7203."}}},
			},
		},
	}
}

// TS-30-35 at its source: a run that did what the live test requires passes, and
// each thing it requires is reported when it does not hold.
func TestAssertLiveRunReportsEachViolation_TS30_35(t *testing.T) {
	for _, p := range providerTable {
		rec := &recorder{}
		assertLiveRun(rec, p, goodLiveRun(t, p))
		if rec.failed() {
			t.Errorf("%s: a run that did everything was reported: %v", p.name, rec.all())
		}
		paid := p.name != "ollama"
		for _, v := range []struct {
			name   string
			mutate func(r *liveRun)
			want   string
			only   bool // applies to this provider's cost kind
		}{
			{"stopped on the turn bound", func(r *liveRun) { r.res.StopReason = core.RunStopMaxTurns }, "ended", true},
			{"ended with an error", func(r *liveRun) { r.err = core.ErrMaxTurns }, "ended", true},
			{"no read_file result", func(r *liveRun) { r.res.Messages = r.res.Messages[:2] }, "read_file", true},
			{"read_file failed", func(r *liveRun) { r.res.Messages[2] = core.ToolResultMessage{ToolName: "read_file", IsError: true} }, "read_file", true},
			{"another tool was read", func(r *liveRun) { r.res.Messages[2] = core.ToolResultMessage{ToolName: "list_files"} }, "read_file", true},
			{"the word is missing", func(r *liveRun) { r.word = "other-word" }, "word", true},
			{"another provider", func(r *liveRun) { r.resolution.Provider = "elsewhere" }, "Resolution", true},
			{"another tier", func(r *liveRun) { r.resolution.Tier = catalog.TierSimple }, "Resolution", true},
			{"no tier", func(r *liveRun) { r.resolution.Tier = "" }, "Resolution", true},
			{"another model", func(r *liveRun) { r.resolution.Model.ID = "elsewhere" }, "Resolution", true},
			{"another effort", func(r *liveRun) { r.resolution.Effort = core.EffortMax }, "Resolution", true},
			{"no input tokens", func(r *liveRun) { r.res.Usage.InputTokens = 0 }, "tokens", true},
			{"no output tokens", func(r *liveRun) { r.res.Usage.OutputTokens = 0 }, "tokens", true},
			{"unpriced", func(r *liveRun) { r.res.Usage.Unpriced = true }, "unpriced", true},
			{"no cost", func(r *liveRun) { r.res.Usage.CostUSD = 0 }, "cost", paid},
			{"a negative cost", func(r *liveRun) { r.res.Usage.CostUSD = -1 }, "cost", true},
			{"a cost on the local machine", func(r *liveRun) { r.res.Usage.CostUSD = 0.01 }, "cost", !paid},
		} {
			if !v.only {
				continue
			}
			rec := &recorder{}
			r := goodLiveRun(t, p)
			r.res.Messages = slices.Clone(r.res.Messages)
			v.mutate(&r)
			assertLiveRun(rec, p, r)
			if !strings.Contains(strings.Join(rec.all(), "\n"), v.want) {
				t.Errorf("%s, %s: reported %q, want a failure mentioning %q", p.name, v.name, rec.all(), v.want)
			}
		}
	}
}

// logRecorder is a recorder that also keeps what is logged.
type logRecorder struct {
	recorder
	logs []string
}

func (l *logRecorder) Logf(format string, args ...any) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

// assertLiveRun logs what the run spent: the model, the effort, the turns, the
// tokens and the cost.
func TestAssertLiveRunLogsWhatTheRunSpent_TS30_35(t *testing.T) {
	p := providerByName("openrouter")
	run := goodLiveRun(t, p)
	run.res.Usage = core.Usage{InputTokens: 1234, OutputTokens: 567, CostUSD: 0.01234}
	rec := &logRecorder{}
	assertLiveRun(rec, p, run)
	if rec.failed() || len(rec.logs) != 1 {
		t.Fatalf("reported %v and logged %q, want a pass and one log line", rec.all(), rec.logs)
	}
	for _, want := range []string{run.resolution.Model.ID, string(run.resolution.Effort), "2 turns", "1234 input", "567 output", "$0.01234"} {
		if !strings.Contains(rec.logs[0], want) {
			t.Errorf("the log line %q does not mention %q", rec.logs[0], want)
		}
	}
}

// The word in the file is fresh on every run and cannot be guessed from the
// task, so that finding it in the answer shows the answer came from the file.
func TestFreshWordIsFreshAndShaped(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		w := freshWord()
		if !regexp.MustCompile(`^[a-z]+-[0-9]{4}$`).MatchString(w) {
			t.Fatalf("freshWord() = %q, want a lower-case word, a hyphen and four digits", w)
		}
		seen[w] = true
	}
	if len(seen) < 100 {
		t.Errorf("200 words gave %d different ones, want them almost all different", len(seen))
	}
}

// wordInFile is what freshWord makes: the word the file holds.
var wordInFile = regexp.MustCompile(`[a-z]+-[0-9]{4}`)

// answeringServer plays one vendor's service for a live run: it asks for
// read_file on secret.txt and then answers with the word the result holds. extra
// answers every other path.
func answeringServer(t *testing.T, v vendor, extra http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != v.path {
			extra(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s, err := v.decode(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		turn := logicalTurn{blocks: []block{toolBlock("call_live_1", "read_file", `{"path":"secret.txt"}`)}, end: endToolStop}
		if len(s.Results) > 0 {
			turn = logicalTurn{blocks: []block{textBlock("The word is " + wordInFile.FindString(s.Results[0].Text) + ".")}}
		}
		v.render(w, s.Model, turn)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLive itself, end to end and offline: with a provider's variables pointing
// at a loopback server that plays the service, each subtest does not skip, runs
// the tool loop, finds the random word in the answer and passes its checks.
func TestSubtestsRunEndToEndWhenTheirPrerequisitesHold_TS30_34(t *testing.T) {
	t.Parallel()
	row := ollamaRow(t)
	notFound := func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	for _, c := range []struct {
		provider string
		extra    http.HandlerFunc
		env      func(url string) []string
	}{
		{"anthropic", notFound, func(url string) []string { return []string{"ANTHROPIC_API_KEY=live-key", "ANTHROPIC_BASE_URL=" + url} }},
		{"openrouter", notFound, func(url string) []string {
			return []string{"OPENROUTER_API_KEY=live-key", "OPENROUTER_BASE_URL=" + url + "/api/v1"}
		}},
		{"ollama", tagsAnswer(row.ID), func(url string) []string { return []string{"OLLAMA_HOST=" + url} }},
	} {
		t.Run(c.provider, func(t *testing.T) {
			t.Parallel()
			p := providerByName(c.provider)
			sel, err := catalog.Select(p.name, "STANDARD", "")
			if err != nil {
				t.Fatal(err)
			}
			srv := answeringServer(t, p.vendor, c.extra)
			events := live(t, c.provider, c.env(srv.URL)...)
			if got := action(events, "TestLive/"+c.provider); got != "pass" {
				t.Errorf("TestLive/%s ended %q, want pass:\n%s", c.provider, got, output(events, "TestLive/"+c.provider))
			}
			if out := output(events, "TestLive/"+c.provider); !strings.Contains(out, "model "+sel.Model.ID) {
				t.Errorf("the run logged no line for the model %s:\n%s", sel.Model.ID, out)
			}
		})
	}
}
