// Command deferred shows deferred submission: a run that ends holding a
// RECEIPT instead of an answer, a process that exits, and a later process
// that redeems the receipt and carries on from the answer.
//
//	go run ./examples/deferred
//
// It runs with no API key and no network. No wire API shipped with AgentKit
// accepts deferred submissions today — the example probes all five to show
// it — so the capability is demonstrated the only honest way: with a provider
// of your own. The `batch` provider below fronts a simulated job queue (the
// shape of a vendor batch API) and delegates the actual answering to a
// scripted faux model. Everything on the agent side — the probe, the clean
// RunStopDeferred end, the handle in the session log, the poll-after guard,
// RedeemDeferred running the loop from the answer — is the shipped code.
//
// See examples/deferred/README.md for a walkthrough.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/provider/google"
	"github.com/agentfox/agentkit-go/provider/ollama"
	"github.com/agentfox/agentkit-go/provider/openai"
	"github.com/agentfox/agentkit-go/provider/openairesponses"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/session"
	"github.com/agentfox/agentkit-go/stop"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// The provider an embedder writes. A deferred-capable wire is an ordinary
// core.APIProvider with FetchDeferred set: Stream accepts a submission and
// answers with a handle; FetchDeferred turns a handle back into a message.

// batchAPI is the wire id this provider registers under.
const batchAPI core.API = "batch"

// pollAfter is how long the queue says to wait before asking. RedeemDeferred
// refuses to ask sooner, without a request.
const pollAfter = 300 * time.Millisecond

type job struct {
	req    core.Request
	answer *core.AssistantMessage
}

// batchQueue stands in for a vendor's batch service: it outlives the process
// that submitted to it. A real one is a remote API; the handle's ID is the
// vendor's job id.
type batchQueue struct {
	mu    sync.Mutex
	jobs  map[string]*job
	seq   int
	model core.StreamFunc // what actually answers, when the worker gets to it
}

func (q *batchQueue) provider() core.APIProvider {
	return core.APIProvider{API: batchAPI, Stream: q.stream, FetchDeferred: q.fetch}
}

func (q *batchQueue) stream(ctx context.Context, m *core.Model, req core.Request, o core.ProviderStreamOptions) *core.EventStream {
	if req.Deferred == nil {
		return q.model(ctx, m, req, o) // not asked to defer: answer live
	}
	q.mu.Lock()
	q.seq++
	id := fmt.Sprintf("job_%03d", q.seq)
	q.jobs[id] = &job{req: req}
	q.mu.Unlock()

	window := req.Deferred.Window
	if window == 0 {
		window = 24 * time.Hour
	}
	// The receipt, in place of content. It carries its own provenance so a
	// later binary cannot redeem it against the wrong model.
	msg := core.AssistantMessage{
		StopReason: core.StopReasonDeferred,
		Deferred: &core.DeferredHandle{
			Provider: m.Provider, API: m.API, ModelID: m.ID, ID: id,
			ExpiresAt:   time.Now().Add(window),
			PollAfterMS: int(pollAfter / time.Millisecond),
			Data:        json.RawMessage(`{"queue":"overnight"}`),
		},
		Provider: m.Provider, API: m.API, Model: m.ID, Timestamp: time.Now(),
	}
	s := core.NewEventStream(core.StreamOptions{})
	s.Push(core.MessageEndEvent{Message: msg})
	s.End(core.StreamResult{Message: &msg})
	return s
}

func (q *batchQueue) fetch(ctx context.Context, m *core.Model, h core.DeferredHandle, o core.ProviderStreamOptions) *core.EventStream {
	q.mu.Lock()
	j, ok := q.jobs[h.ID]
	q.mu.Unlock()
	switch {
	case !ok:
		return core.ErrorStream(nil, fmt.Errorf("batch: no job %q", h.ID))
	case j.answer == nil:
		return core.ErrorStream(nil, fmt.Errorf("batch: job %s is still running", h.ID))
	}
	msg := *j.answer
	s := core.NewEventStream(core.StreamOptions{})
	s.Push(core.MessageEndEvent{Message: msg})
	s.End(core.StreamResult{Message: &msg})
	return s
}

// work is the vendor's worker finishing the queue, some time later.
func (q *batchQueue) work(ctx context.Context, m *core.Model) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.answer == nil {
			req := j.req
			req.Deferred = nil
			j.answer = q.model(ctx, m, req, core.ProviderStreamOptions{}).Result()
		}
	}
}

// ---------------------------------------------------------------------------

func run(w io.Writer) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "agentkit-deferred-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "session.jsonl")

	// ------------------------------------------------------------------ 1
	section(w, "1. probe before you submit")
	shipped := core.AgentConfig{}
	agentkit.RegisterDefaults(&shipped,
		anthropic.Provider(anthropic.Options{}),
		openai.Provider(openai.Options{}),
		openairesponses.Provider(openairesponses.Options{}),
		google.Provider(google.Options{}),
		ollama.Provider(ollama.Options{}),
	)
	for _, api := range []core.API{anthropic.API, openai.API, openairesponses.API, google.API, ollama.API} {
		fmt.Fprintf(w, "  %-20s SupportsDeferred = %v\n", api, shipped.Providers.SupportsDeferred(api))
	}
	fmt.Fprintln(w, "  No shipped wire registers FetchDeferred. A provider that ignores")
	fmt.Fprintln(w, "  RequestOptions.Deferred just answers immediately, so always probe first.")

	// The model's answers, as the batch worker will produce them: a tool call
	// first (the common case for an agent), then the final answer.
	answers := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{
			faux.FauxText("I'll count the open incidents first."),
			faux.FauxToolCall("call_1", "count_incidents", `{"status":"open"}`),
		}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(
			"Weekly report: 3 open incidents, none above sev-2.")}},
	)
	queue := &batchQueue{jobs: map[string]*job{}, model: answers.Stream}

	model := faux.Model()
	model.API, model.Provider, model.ID = batchAPI, "example", "batch-1"
	cfg := core.AgentConfig{
		Model:        model,
		SystemPrompt: "You write the weekly incident report.",
		StopPolicy:   stop.AfterTurns(6),
		Providers:    core.ProviderRegistry{batchAPI: queue.provider()},
	}
	probe, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  %-20s SupportsDeferred = %v   ← the provider below\n", batchAPI, probe.SupportsDeferred())

	// ------------------------------------------------------------------ 2
	section(w, "2. process 1 submits and exits")
	store, resume, err := session.OpenOrCreate(logPath, session.Options{})
	if err != nil {
		return err
	}
	cfg1 := cfg
	cfg1.SessionStore = store
	// The opt-in. It applies to every model call this agent makes while set.
	cfg1.RequestOptions.Deferred = &core.DeferredRequest{Window: 24 * time.Hour}
	agent, err := agentkit.NewAgentFromSession(cfg1, resume, sameModel(model))
	if err != nil {
		return err
	}
	res, err := agent.Run(ctx, "Write this week's incident report.")
	if err != nil {
		return err
	}
	h, ok := res.DeferredHandle()
	fmt.Fprintf(w, "  run ended: stop=%s, err=nil, handle present=%v\n", res.StopReason, ok)
	fmt.Fprintf(w, "  handle: id=%s model=%s/%s expires in %s, poll after %dms, data=%s\n",
		h.ID, h.API, h.ModelID, time.Until(h.ExpiresAt).Round(time.Hour), h.PollAfterMS, h.Data)
	_ = store.Close()
	fmt.Fprintln(w, "  ...process 1 exits. The handle is in the session log, not just in memory.")

	// ------------------------------------------------------------------ 3
	section(w, "3. process 2 finds the handle in the log")
	store2, resume2, err := session.OpenOrCreate(logPath, session.Options{})
	if err != nil {
		return err
	}
	defer store2.Close()
	h2, ok := pendingHandle(resume2.Messages)
	fmt.Fprintf(w, "  folded %d messages; last assistant turn holds handle %s: %v\n",
		len(resume2.Messages), h2.ID, ok)

	// Redemption runs the loop from the answer, so the agent needs the
	// tools the answer may call. Deferred is NOT set here: the turns after
	// the redeemed one run live. Leave it set to defer those too.
	cfg2 := cfg
	cfg2.SessionStore = store2
	agent2, err := agentkit.NewAgentFromSession(cfg2, resume2, sameModel(model))
	if err != nil {
		return err
	}
	if err := agent2.RegisterTool(countIncidents()); err != nil {
		return err
	}

	// ------------------------------------------------------------------ 4
	section(w, "4. redeem: too early, not ready, ready")
	// IssuedAt was stamped when process 1 received the handle and saved with
	// it, so the poll-after delay counts across the restart.
	if _, err := agent2.RedeemDeferred(ctx, h2); err != nil {
		fmt.Fprintf(w, "  now:          %s\n", trimTime(err.Error()))
	}
	time.Sleep(pollAfter + 10*time.Millisecond)
	if _, err := agent2.RedeemDeferred(ctx, h2); err != nil {
		fmt.Fprintf(w, "  after %v: %v\n", pollAfter, err)
	}
	fmt.Fprintln(w, "  (a failed fetch records nothing, so the same handle can be redeemed again)")

	queue.work(ctx, model) // the vendor's worker finishes the job
	res2, err := agent2.RedeemDeferred(ctx, h2)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "  after the job ran: %d turns, stop=%s\n", res2.TurnCount, res2.StopReason)
	for _, m := range res2.Messages {
		fmt.Fprintf(w, "    %s\n", describe(m))
	}
	fmt.Fprintln(w, "\n  The redeemed answer was a tool call. RedeemDeferred appended it, ran the")
	fmt.Fprintln(w, "  tool, and carried on to the model's next turn — exactly as a live turn would.")

	// ------------------------------------------------------------------ 5
	section(w, "5. what the log holds now")
	branch, _ := store2.Branch(store2.Head())
	for _, e := range branch {
		if e.Type == core.EntryMessage {
			fmt.Fprintf(w, "  %s\n", describe(e.Message.Message))
		}
	}
	return nil
}

// pendingHandle is how an application finds an unredeemed handle after a
// restart: the newest assistant message, if it is a receipt.
func pendingHandle(msgs core.Messages) (core.DeferredHandle, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if am, ok := msgs[i].(core.AssistantMessage); ok {
			if am.Deferred != nil && !am.Deferred.IsZero() {
				return *am.Deferred, true
			}
			return core.DeferredHandle{}, false
		}
	}
	return core.DeferredHandle{}, false
}

func countIncidents() core.Tool {
	return core.Tool{
		Name:        "count_incidents",
		Description: "Count incidents by status.",
		InputSchema: schema.Object(schema.Prop("status", schema.String("open or closed"))),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"count":3,"max_severity":"sev-2"}`), nil
		},
	}
}

func sameModel(m *core.Model) func(string, core.API, string) (*core.Model, error) {
	return func(string, core.API, string) (*core.Model, error) { return m, nil }
}

func describe(m core.Message) string {
	switch v := m.(type) {
	case core.UserMessage:
		return "user        " + v.Content.Text()
	case core.AssistantMessage:
		if v.Deferred != nil {
			return fmt.Sprintf("assistant   (receipt %s, stop %s)", v.Deferred.ID, v.StopReason)
		}
		var parts []string
		if t := v.Content.Text(); t != "" {
			parts = append(parts, fmt.Sprintf("%q", t))
		}
		for _, c := range core.ExtractToolUse(&v) {
			parts = append(parts, fmt.Sprintf("calls %s%s", c.Name, c.Input))
		}
		return "assistant   " + strings.Join(parts, " + ")
	case core.ToolResultMessage:
		return "tool_result " + v.Content.Text()
	}
	return string(m.Role())
}

// trimTime removes the wall-clock timestamp from the refusal so the output is
// stable from run to run.
func trimTime(s string) string {
	if i := strings.Index(s, " is not due until "); i >= 0 {
		if j := strings.Index(s, " (poll_after_ms"); j > i {
			return s[:i] + " is not due yet" + s[j:]
		}
	}
	return s
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 70-len(title))))
}
