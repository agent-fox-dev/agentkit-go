// Command middleware shows the shipped model-call middleware — Retry,
// RateLimit, Budget, Caching and Tracing — what each one is for, and why the
// order they are registered in matters.
//
//	go run ./examples/middleware
//
// Everything runs with no API key and no network: the real loop drives a
// scripted faux provider, and each section scripts the failure or the repeat
// the middleware exists for. With a key, --real puts the whole recommended
// stack in front of a real model and asks it the same question twice; the
// second answer is a cache hit that costs nothing:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/middleware --real
//	AGENTKIT_MODEL=openai/gpt-5.6-terra go run ./examples/middleware --real
//
// See examples/middleware/README.md for a walkthrough.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/catalog"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/middleware"
	"github.com/agentfox/agentkit-go/provider"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/provider/google"
	"github.com/agentfox/agentkit-go/provider/ollama"
	"github.com/agentfox/agentkit-go/provider/openai"
	"github.com/agentfox/agentkit-go/provider/openairesponses"
	"github.com/agentfox/agentkit-go/stop"
)

func main() {
	real := flag.Bool("real", false, "also run the full stack against a real model (needs a credential)")
	flag.Parse()
	if err := run(os.Stdout, *real); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, real bool) error {
	ctx := context.Background()
	steps := []func(context.Context, io.Writer) error{
		demoOrdering, demoRetry, demoRateLimit, demoBudget, demoCaching, demoTracing,
	}
	for _, step := range steps {
		if err := step(ctx, w); err != nil {
			return err
		}
	}
	if real {
		return demoReal(ctx, w)
	}
	section(w, "7. the whole stack against a real model")
	fmt.Fprintln(w, "  skipped: pass --real (and set a credential) to run it.")
	return nil
}

// ---------------------------------------------------------------------------

// logging is a middleware of your own: it records the order calls pass
// through it. A middleware is just func(next core.Handler) core.Handler.
func logging(label string, log *[]string) core.Middleware {
	return func(next core.Handler) core.Handler {
		return func(ctx context.Context, req core.Request) *core.EventStream {
			*log = append(*log, label+" →")
			s := next(ctx, req)
			*log = append(*log, "← "+label)
			return s
		}
	}
}

func demoOrdering(ctx context.Context, w io.Writer) error {
	section(w, "1. the last registered middleware is the outermost")
	var log []string
	p := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}})
	cfg := baseConfig(p)
	cfg.Middleware = []core.Middleware{
		logging("A (registered first)", &log),
		logging("B", &log),
		logging("C (registered last)", &log),
	}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	if _, err := agent.Run(ctx, "hello"); err != nil {
		return err
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(log, "  "))
	fmt.Fprintln(w, "  C sees the request first and the response last. Register the middleware")
	fmt.Fprintln(w, "  that must see everything (tracing) LAST, and the one that must run once per")
	fmt.Fprintln(w, "  network attempt (rate limiting) FIRST.")
	return nil
}

// ---------------------------------------------------------------------------

func demoRetry(ctx context.Context, w io.Writer) error {
	section(w, "2. Retry: a transient failure is retried, a billing one is not")

	// The first attempt fails mid-stream with a 503 after it was billed. A
	// real provider produces exactly this: an assistant message whose stop
	// reason is "error" and whose text names the failure.
	p := faux.New(
		faux.Turn{Err: errors.New("503 service unavailable"), Usage: cost(0.002)},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("Recovered on the second attempt.")}, Usage: cost(0.004)},
	)
	cfg := baseConfig(p)
	cfg.Middleware = []core.Middleware{middleware.Retry(middleware.RetryOptions{
		MaxAttempts: 3, // counts the first attempt; the default is 1 — no retries
		BaseDelay:   200 * time.Millisecond,
		// Sleep and Rand are injectable so this example (and your tests)
		// neither wait nor depend on jitter. Rand 0 means "no jitter"; real
		// jitter only ever shortens the delay, by up to 25%.
		Sleep: func(_ context.Context, d time.Duration) error {
			fmt.Fprintf(w, "  retry: backing off %v before attempt 2\n", d)
			return nil
		},
		Rand: func() float64 { return 0 },
	})}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	res, err := agent.Run(ctx, "summarize the incident")
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  answer: %q after %d provider calls\n", res.FinalText(), p.Calls())
	fmt.Fprintf(w, "  Agent.Usage() = $%.3f — the discarded attempt was billed, so it is counted\n",
		agent.Usage().CostUSD)

	fmt.Fprintln(w, "\n  middleware.Retryable classifies the error TEXT, denylist first:")
	for _, msg := range []string{
		"503 service unavailable",
		"stream ended before message_stop",
		"429 rate limit: insufficient_quota for this billing period",
		"400 invalid_request_error: messages: field required",
	} {
		m := &core.AssistantMessage{StopReason: core.StopReasonError, ErrorMessage: msg}
		fmt.Fprintf(w, "    %-5v %s\n", middleware.Retryable(m), msg)
	}
	return nil
}

// ---------------------------------------------------------------------------

func demoRateLimit(ctx context.Context, w io.Writer) error {
	section(w, "3. RateLimit: a token bucket over model calls")
	turns := make([]faux.Turn, 5)
	for i := range turns {
		turns[i] = faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}}
	}
	p := faux.New(turns...)
	cfg := baseConfig(p)
	// 10 calls a second on average, at most 1 at once. Share ONE limiter
	// value across every agent that spends the same quota — a limiter per
	// agent limits nothing.
	limiter := middleware.RateLimit(10, 1)
	cfg.Middleware = []core.Middleware{limiter}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	start := time.Now()
	for i := 0; i < 5; i++ {
		if _, err := agent.Run(ctx, fmt.Sprintf("call %d", i)); err != nil {
			return err
		}
	}
	el := time.Since(start)
	fmt.Fprintf(w, "  5 calls at 10/s with burst 1: %s (the first is free, then ~100ms apart)\n",
		roughly(el))
	return nil
}

func roughly(d time.Duration) string {
	if d >= 350*time.Millisecond {
		return "~400ms"
	}
	return d.Round(10 * time.Millisecond).String()
}

// ---------------------------------------------------------------------------

func demoBudget(ctx context.Context, w io.Writer) error {
	section(w, "4. Budget: refuse the NEXT call once the money is spent")
	turns := make([]faux.Turn, 4)
	for i := range turns {
		turns[i] = faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("a $0.004 answer")}, Usage: cost(0.004)}
	}
	p := faux.New(turns...)
	cfg := baseConfig(p)

	// Budget reads spend through a function, and the spend lives on the
	// agent — which does not exist yet. Bind it late: the closure runs on
	// every call, long after the agent is assigned.
	var agent *agentkit.Agent
	cfg.Middleware = []core.Middleware{
		middleware.Budget(0.010, func() core.Usage { return agent.Usage() }),
	}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	for i := 1; i <= 4; i++ {
		res, err := agent.Run(ctx, fmt.Sprintf("question %d", i))
		status := "ok"
		if err != nil {
			status = "refused: " + firstLine(err.Error())
		}
		fmt.Fprintf(w, "  call %d: spent so far $%.3f → %s (stop: %s)\n",
			i, agent.Usage().CostUSD, status, res.StopReason)
	}
	fmt.Fprintf(w, "  provider calls made: %d of 4 — the refused one never left the process\n", p.Calls())
	fmt.Fprintln(w, "  stop.OverBudget ends a run cleanly AFTER a turn; Budget refuses BEFORE one.")
	fmt.Fprintln(w, "  Use both: the policy for the normal stop, the gate as the hard ceiling.")
	return nil
}

// ---------------------------------------------------------------------------

func demoCaching(ctx context.Context, w io.Writer) error {
	section(w, "5. Caching: identical requests are answered from memory")

	// A stateless classifier: a FRESH agent per input, so the same input
	// produces byte-identical requests. One Caching value and one meter are
	// shared across all of them; that is where the dedup lives.
	p := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("bug")}, Usage: cost(0.003)},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("feature")}, Usage: cost(0.003)},
	)
	meter := middleware.NewCacheMeter()
	cache := middleware.Caching(middleware.CacheOptions{MaxSize: 256, Meter: meter})

	classify := func(text string) (string, error) {
		cfg := baseConfig(p)
		cfg.SystemPrompt = "Classify the issue as bug or feature. One word."
		cfg.Middleware = []core.Middleware{cache}
		agent, err := agentkit.NewAgent(cfg)
		if err != nil {
			return "", err
		}
		res, err := agent.Run(ctx, text)
		return res.FinalText(), err
	}
	for _, in := range []string{
		"The export button crashes on empty tables.",
		"Please add dark mode.",
		"The export button crashes on empty tables.", // a repeat
	} {
		label, err := classify(in)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "  %-45q → %s\n", in, label)
	}
	s := meter.Stats()
	fmt.Fprintf(w, "  provider calls: %d; Level 2 hits %d, misses %d, saved $%.3f\n",
		p.Calls(), s.Hits, s.Misses, s.EstimatedSavingsUSD)
	fmt.Fprintln(w, "  (A request with Temperature > 0 is never served from cache: replaying a")
	fmt.Fprintln(w, "   sample the caller asked to be random is a bug, not an optimization.)")

	// Level 1 is the PROVIDER's prompt cache. The agent meters it from the
	// usage each turn reports, so Agent.CacheStats() needs no middleware.
	fmt.Fprintln(w, "\n  Agent.CacheStats() also carries Level 1, the provider's own prompt cache:")
	priced := faux.Model()
	priced.Cost = core.Cost{Input: 3, Output: 15, CacheRead: 0.30} // $ per 1M tokens
	u := cost(0.01)
	u.SetField(core.UsageInputTokens, 2_000)
	u.SetField(core.UsageCacheReadTokens, 50_000)
	p2 := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}, Usage: u})
	cfg := baseConfig(p2)
	cfg.Model = priced
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	if _, err := agent.Run(ctx, "continue"); err != nil {
		return err
	}
	cs := agent.CacheStats()
	fmt.Fprintf(w, "  provider reported %d cache-read tokens → saved $%.3f at this model's rates\n",
		cs.ProviderCacheReadTokens, cs.EstimatedSavingsUSD)
	return nil
}

// ---------------------------------------------------------------------------

// printTracer is a core.Tracer that writes each span when it ends. An
// OpenTelemetry adapter has the same shape: StartSpan opens, Span.End closes.
type printTracer struct {
	w  io.Writer
	mu sync.Mutex
}

func (t *printTracer) StartSpan(name string, fn func(core.Span) error) error {
	return fn(&printSpan{t: t, name: name})
}

type printSpan struct {
	t     *printTracer
	name  string
	attrs map[string]any
}

func (s *printSpan) SetAttributes(kv map[string]any) {
	if s.attrs == nil {
		s.attrs = map[string]any{}
	}
	for k, v := range kv {
		s.attrs[k] = v
	}
}
func (s *printSpan) SetStatus(error)                 {}
func (s *printSpan) AddEvent(string, map[string]any) {}
func (s *printSpan) End() {
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	keys := make([]string, 0, len(s.attrs))
	for k := range s.attrs {
		if k == "cache.fingerprint" {
			continue // a sha256, long and not interesting here
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, s.attrs[k]))
	}
	fmt.Fprintf(s.t.w, "  span %s: %s\n", s.name, strings.Join(parts, " "))
}

func demoTracing(ctx context.Context, w io.Writer) error {
	section(w, "6. Tracing: one span per model call, cache decision included")
	p := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("bug")}, Usage: cost(0.003)})
	tracer := &printTracer{w: w}
	cache := middleware.Caching(middleware.CacheOptions{})

	for i := 0; i < 2; i++ {
		cfg := baseConfig(p)
		// Tracing registered AFTER Caching, so it is outside it and can see
		// whether the call was a hit. The other way round the span still has
		// every model attribute, just no cache.* ones.
		cfg.Middleware = []core.Middleware{cache, middleware.Tracing(tracer)}
		agent, err := agentkit.NewAgent(cfg)
		if err != nil {
			return err
		}
		if _, err := agent.Run(ctx, "The export button crashes."); err != nil {
			return err
		}
	}
	fmt.Fprintln(w, "  Tool calls get their own spans from AgentConfig.Tracer — see examples/observability.")
	return nil
}

// ---------------------------------------------------------------------------

func demoReal(ctx context.Context, w io.Writer) error {
	section(w, "7. the whole stack against a real model")
	model, err := catalog.ResolveModel(modelSpec())
	if err != nil {
		return err
	}
	if err := checkCredentials(model); err != nil {
		return err
	}

	// Shared across every agent that spends this quota and this money.
	tracer := &printTracer{w: w}
	limiter := middleware.RateLimit(2, 2)
	retry := middleware.Retry(middleware.RetryOptions{MaxAttempts: 3})
	meter := middleware.NewCacheMeter()
	cache := middleware.Caching(middleware.CacheOptions{Meter: meter})
	var spent struct {
		sync.Mutex
		u core.Usage
	}
	budget := middleware.Budget(0.05, func() core.Usage { spent.Lock(); defer spent.Unlock(); return spent.u })
	// network counts what actually reached the provider. It is innermost, so
	// it sees every attempt and nothing the cache answered.
	var network int
	counter := func(next core.Handler) core.Handler {
		return func(ctx context.Context, req core.Request) *core.EventStream {
			network++
			return next(ctx, req)
		}
	}

	for i := 0; i < 2; i++ {
		cfg := core.AgentConfig{Model: model}
		agentkit.RegisterDefaults(&cfg,
			anthropic.Provider(anthropic.Options{}),
			openai.Provider(openai.Options{}),
			openairesponses.Provider(openairesponses.Options{}),
			google.Provider(google.Options{}),
			ollama.Provider(ollama.Options{}),
		)
		cfg.SystemPrompt = "Classify the issue as bug or feature. Answer with one word."
		cfg.StopPolicy = stop.Any(stop.AfterTurns(2), stop.OverBudget(0.05))
		zero := 0.0
		cfg.Temperature = &zero // a sampled answer is never cached
		cfg.Middleware = []core.Middleware{
			counter,                    // (example only) counts calls that reach the provider
			limiter,                    // every network attempt, retries included, waits its turn
			retry,                      // retries what the provider failed transiently
			budget,                     // refuses a call once the money is spent
			cache,                      // a hit makes no call, so it is served even past the budget
			middleware.Tracing(tracer), // outermost: sees every call and the cache decision
		}
		agent, err := agentkit.NewAgent(cfg)
		if err != nil {
			return err
		}
		res, err := agent.Run(ctx, "The export button crashes on empty tables.")
		if err != nil {
			return err
		}
		spent.Lock()
		spent.u = spent.u.Add(res.Usage)
		spent.Unlock()
		fmt.Fprintf(w, "  run %d: %q\n", i+1, strings.TrimSpace(res.FinalText()))
	}
	s := meter.Stats()
	fmt.Fprintf(w, "  provider calls: %d; cache hits %d, misses %d; avoided $%.5f\n",
		network, s.Hits, s.Misses, s.EstimatedSavingsUSD)
	fmt.Fprintln(w, "  The second run was answered by the cache: same request bytes, no request sent.")
	return nil
}

// ---------------------------------------------------------------------------

func baseConfig(p *faux.Provider) core.AgentConfig {
	return core.AgentConfig{
		Model:        faux.Model(),
		SystemPrompt: "You are a helpful assistant.",
		StopPolicy:   stop.AfterTurns(4),
		Providers:    core.ProviderRegistry{faux.API: p.APIProvider()},
	}
}

// cost is a reported usage carrying only a price, as a provider would.
func cost(usd float64) core.Usage {
	var u core.Usage
	u.SetField(core.UsageInputTokens, 100)
	u.SetField(core.UsageOutputTokens, 20)
	u.SetCost(usd)
	return u
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 70-len(title))))
}

// modelSpec is "vendor/model-id", or a bare id when it is unambiguous.
func modelSpec() string {
	if s := os.Getenv("AGENTKIT_MODEL"); s != "" {
		return s
	}
	return "anthropic/claude-sonnet-5"
}

// checkCredentials fails BEFORE the request with a message naming the variable
// to set, rather than after a 401 that names none of them. "ambient" (an
// instance role, ADC) passes; only "none" fails.
func checkCredentials(m *core.Model) error {
	auth := provider.ResolveAuth(authFor(m), provider.Env{})
	if auth.State != provider.CredentialNone {
		return nil
	}
	return fmt.Errorf("no credential for vendor %q: set one of %s (see examples/README.md)",
		m.Provider, strings.Join(varNames(authFor(m)), ", "))
}

func authFor(m *core.Model) provider.VendorAuth {
	switch m.API {
	case anthropic.API:
		return anthropic.VendorAuth
	case google.API:
		return google.VendorAuth
	case ollama.API:
		return ollama.VendorAuth
	default:
		return openai.AuthFor(m.Provider)
	}
}

func varNames(v provider.VendorAuth) []string {
	out := make([]string, 0, len(v.Vars)+1)
	for _, e := range v.Vars {
		out = append(out, e.Name)
	}
	if v.BaseURLVar != "" {
		out = append(out, v.BaseURLVar+" (for a gateway or a local server)")
	}
	if len(out) == 0 {
		out = append(out, "a vendor-specific API key")
	}
	return out
}
