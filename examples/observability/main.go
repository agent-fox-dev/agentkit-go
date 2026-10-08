// Command observability shows the ways an application watches an agent:
// lifecycle hooks, the audit trail, spans for tool and model calls, and the
// event stream serialized as JSON for a log or a socket.
//
//	go run ./examples/observability
//
// It runs with no API key and no network: the real loop drives a scripted
// faux provider through a three-turn run in which one tool call succeeds, one
// is refused by the host's interceptor, and one hook panics — so every
// channel has something to report.
//
// See examples/observability/README.md for a walkthrough.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/middleware"
	"github.com/agentfox/agentkit-go/provider/faux"
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

// script is the run every section observes: look an order up, try to refund
// it (the host refuses), look it up again, answer.
func script() *faux.Provider {
	return faux.New(
		faux.Turn{
			Blocks: []core.ContentBlock{
				faux.FauxText("Let me check the order and refund it."),
				faux.FauxToolCall("call_1", "lookup_order", `{"id":"A-1001"}`),
				faux.FauxToolCall("call_2", "refund_order", `{"id":"A-1001","amount_eur":49}`),
			},
			StopReason: core.StopReasonToolUse, Usage: usage(420, 60, 0.0021),
		},
		faux.Turn{
			Blocks:     []core.ContentBlock{faux.FauxToolCall("call_3", "lookup_order", `{"id":"A-1001"}`)},
			StopReason: core.StopReasonToolUse, Usage: usage(610, 30, 0.0023),
		},
		faux.Turn{
			Blocks:     []core.ContentBlock{faux.FauxText("Order A-1001 shipped; refunds need a human, so I have not issued one.")},
			StopReason: core.StopReasonStop, Usage: usage(700, 40, 0.0027),
		},
	)
}

func tools() []core.Tool {
	lookup := core.Tool{
		Name:        "lookup_order",
		Description: "Look up an order by id.",
		InputSchema: schema.Object(schema.Prop("id", schema.String("Order id"))),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			time.Sleep(3 * time.Millisecond) // something for elapsed_ms to measure
			return json.RawMessage(`{"status":"shipped"}`), nil
		},
	}
	refund := core.Tool{
		Name:        "refund_order",
		Description: "Refund an order.",
		InputSchema: schema.Object(
			schema.Prop("id", schema.String("Order id")),
			schema.Prop("amount_eur", schema.Number("Amount")),
		),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`{"refunded":true}`), nil
		},
	}
	return []core.Tool{lookup, refund}
}

func run(w io.Writer) error {
	ctx := context.Background()
	p := script()

	// ---- One tracer for BOTH kinds of span. Tool spans come from
	// AgentConfig.Tracer (middleware never sees a tool call); model-call
	// spans come from middleware.Tracing. The same value in both gives one
	// trace.
	tracer := &recordingTracer{}

	// ---- The audit sink. A real one writes to a log aggregator; this one
	// encodes JSON lines into a buffer.
	var audit bytes.Buffer
	auditEnc := json.NewEncoder(&audit)

	var timeline []string
	note := func(format string, args ...any) { timeline = append(timeline, fmt.Sprintf(format, args...)) }

	cfg := core.AgentConfig{
		Model:        faux.Model(),
		SystemPrompt: "You are a support agent.",
		StopPolicy:   stop.AfterTurns(6),
		Providers:    core.ProviderRegistry{faux.API: p.APIProvider()},
		// SessionID is stamped on every audit event. Set it from your own
		// request or session id so the audit trail joins your other logs.
		SessionID:  "support-7f3a",
		Tracer:     tracer,
		Middleware: []core.Middleware{middleware.Tracing(tracer)},

		// The host's authorization boundary. A refused call is audited like
		// any other, with ErrorCode "blocked_by_policy".
		BeforeToolCall: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ToolName == "refund_order" {
				return core.BeforeToolCallDecision{Block: true, Reason: "refunds need a human"}
			}
			return core.BeforeToolCallDecision{}
		},

		// ---- Hooks: observation, never interception. Each runs with no
		// agent lock held and inside a recover, so a panicking hook is
		// reported to OnError and the run carries on.
		Hooks: core.Hooks{
			OnSessionStart: func(e core.AuditEvent) { note("session start  %s", e.SessionID) },
			OnTurnStart: func(e core.TurnStartEvent) {
				note("turn %d start", e.TurnIndex)
				if e.TurnIndex == 1 {
					panic("a bug in my metrics code")
				}
			},
			OnTurnEnd: func(e core.TurnEndEvent) {
				note("turn %d end    %d tool results, $%.4f, stop %s",
					e.TurnIndex, len(e.ToolResults), e.Usage.CostUSD, e.Message.StopReason)
			},
			OnAgentDone: func(e core.AgentDoneEvent) {
				note("agent done     %d turns, stop %s, run $%.4f", e.Result.TurnCount, e.Result.StopReason, e.Result.Usage.CostUSD)
			},
			OnSessionEnd: func(e core.AuditEvent) { note("session end    stop %s", e.StopReason) },
			OnError:      func(err error) { note("OnError        %v", err) },
			// OnAudit receives EVERY audit event, session start and end
			// included, so one sink needs one registration.
			OnAudit: func(e core.AuditEvent) { _ = auditEnc.Encode(auditRecord(e)) },
		},
	}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	for _, t := range tools() {
		if err := agent.RegisterTool(t); err != nil {
			return err
		}
	}

	// ---- The event stream, serialized. session.EventJSON is the
	// discriminated-union JSON form: a "type" member plus exactly that
	// variant's fields, with messages in the session log's own encoding. It
	// is what you forward to a websocket or a structured log.
	stream, err := agent.Stream(ctx, "Where is order A-1001? Refund it if it is late.")
	if err != nil {
		return err
	}
	var eventLines []string
	counts := map[string]int{}
	turn := 0
	for e := range stream.Events() {
		counts[string(e.EventType())]++
		switch v := e.(type) {
		case core.TextDeltaEvent, core.MessageUpdateEvent, core.ToolInputDeltaEvent:
			continue // high-volume incremental events; counted, not printed
		case core.TurnStartEvent:
			turn = v.TurnIndex
		}
		if _, done := e.(core.AgentDoneEvent); turn > 0 && !done {
			continue // the first turn is enough to show the shape
		}
		b, err := session.EventJSON(e)
		if err != nil {
			return err
		}
		eventLines = append(eventLines, string(b))
	}
	if _, err := stream.RunResult(); err != nil {
		return err
	}

	section(w, "1. hooks: the run's lifecycle")
	for _, l := range timeline {
		fmt.Fprintf(w, "  %s\n", l)
	}
	fmt.Fprintln(w, "  The panic in OnTurnStart was reported and contained; turn 1 still ran.")

	section(w, "2. the audit trail (OnAudit → JSON lines)")
	for _, l := range strings.Split(strings.TrimSpace(audit.String()), "\n") {
		fmt.Fprintf(w, "  %s\n", l)
	}
	fmt.Fprintln(w, "\n  Arguments are HASHED, never logged: the two lookups of A-1001 share a hash,")
	fmt.Fprintln(w, "  so they correlate, and the audit log holds no order data. The same function:")
	fmt.Fprintf(w, "    core.HashArguments(`{\"id\":\"A-1001\"}`) = %s…\n",
		core.HashArguments([]byte(`{"id":"A-1001"}`))[:23])

	section(w, "3. spans: one tracer, model calls and tool calls")
	for _, s := range tracer.spans {
		fmt.Fprintf(w, "  %s\n", s)
	}

	section(w, "4. the event stream as JSON (session.EventJSON)")
	for i, l := range eventLines {
		if i == len(eventLines)-1 {
			fmt.Fprintln(w, "  … turns 1 and 2 elided …")
		}
		fmt.Fprintf(w, "  %s\n", clip(l, 118))
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	fmt.Fprintf(w, "\n  all events, counted: %s\n", strings.Join(parts, " "))
	return nil
}

// auditRecord maps the SDK's audit event onto the application's log schema.
// Only the fields that matter for this kind of event are kept.
func auditRecord(e core.AuditEvent) any {
	type record struct {
		Kind       core.AuditKind `json:"kind"`
		Session    string         `json:"session"`
		Tool       string         `json:"tool,omitempty"`
		ToolUseID  string         `json:"tool_use_id,omitempty"`
		ArgsHash   string         `json:"args_hash,omitempty"`
		IsError    bool           `json:"is_error,omitempty"`
		ErrorCode  string         `json:"error_code,omitempty"`
		ElapsedMS  *int64         `json:"elapsed_ms,omitempty"`
		StopReason string         `json:"stop_reason,omitempty"`
		CostUSD    float64        `json:"cost_usd,omitempty"`
	}
	r := record{Kind: e.Kind, Session: e.SessionID, Tool: e.ToolName, ToolUseID: e.ToolUseID,
		IsError: e.IsError, ErrorCode: e.ErrorCode, StopReason: string(e.StopReason), CostUSD: math.Round(e.Usage.CostUSD*1e6) / 1e6}
	if e.ArgumentsHash != "" {
		r.ArgsHash = e.ArgumentsHash[:23] + "…" // shortened for the terminal only
	}
	if e.Kind == core.AuditToolCall && !e.IsError {
		ms := e.ElapsedMS
		r.ElapsedMS = &ms
	}
	return r
}

// recordingTracer is a core.Tracer that records each span as one line when it
// ends. An OpenTelemetry adapter has the same shape: StartSpan opens a span
// and hands it over; Span.End — not the callback's return — closes it.
type recordingTracer struct {
	mu    sync.Mutex
	spans []string
}

func (t *recordingTracer) StartSpan(name string, fn func(core.Span) error) error {
	return fn(&span{t: t, name: name, attrs: map[string]any{}})
}

type span struct {
	t      *recordingTracer
	name   string
	attrs  map[string]any
	status error
}

func (s *span) SetAttributes(kv map[string]any) {
	for k, v := range kv {
		s.attrs[k] = v
	}
}
func (s *span) SetStatus(err error)             { s.status = err }
func (s *span) AddEvent(string, map[string]any) {}
func (s *span) End() {
	var keep []string
	for _, k := range []string{"tool_name", "is_error", "stop_reason", "input_tokens", "output_tokens", "cost_usd"} {
		if v, ok := s.attrs[k]; ok {
			keep = append(keep, fmt.Sprintf("%s=%v", k, v))
		}
	}
	if s.status != nil {
		keep = append(keep, fmt.Sprintf("status=%q", s.status.Error()))
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.t.spans = append(s.t.spans, fmt.Sprintf("%-22s %s", s.name, strings.Join(keep, " ")))
}

// usage is what a provider would report for one turn.
func usage(in, out int64, usd float64) core.Usage {
	var u core.Usage
	u.SetField(core.UsageInputTokens, in)
	u.SetField(core.UsageOutputTokens, out)
	u.SetCost(usd)
	return u
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n── %s %s\n", title, strings.Repeat("─", max(0, 70-len(title))))
}
