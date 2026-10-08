# observability

An agent in production needs to be watched: what it is doing now (for a UI),
what it did (for an audit), and how long each step took (for a trace).
AgentKit has a separate channel for each, so each consumer gets the volume,
retention and level of detail it needs:

| Channel | Set on | Cadence | For |
|---|---|---|---|
| **Hooks** | `AgentConfig.Hooks` | one call per lifecycle point | metrics, progress, alerting |
| **Audit trail** | `Hooks.OnAudit` (and `OnSessionStart`/`OnSessionEnd`) | one record per *operation* | the durable record of what the agent **did** |
| **Spans** | `AgentConfig.Tracer` (tool calls) + `middleware.Tracing` (model calls) | one per call | tracing, latency |
| **Events** | `agent.Stream(...)` → `session.EventJSON` | streaming, token by token | UIs, sockets, structured logs |

The example runs with **no API key and no network**. A scripted
[`provider/faux`](../../provider/faux) run has three turns. One tool call
succeeds. One is refused by the host's interceptor. One hook panics. Every
channel has something to report.

```bash
go run ./examples/observability
go test ./examples/observability/
```

## What it shows

```
── 1. hooks: the run's lifecycle ─────────────────────────────────────────
  session start  support-7f3a
  turn 0 start
  turn 0 end    2 tool results, $0.0021, stop tool_use
  turn 1 start
  OnError        agentkit: panic in OnTurnStart: a bug in my metrics code
  turn 1 end    1 tool results, $0.0023, stop tool_use
  ...
  agent done     3 turns, stop end_turn, run $0.0071
  session end    stop end_turn

── 2. the audit trail (OnAudit → JSON lines) ───────────────────────────
  {"kind":"session_start","session":"support-7f3a"}
  {"kind":"tool_call",...,"tool":"refund_order",...,"is_error":true,"error_code":"blocked_by_policy"}
  {"kind":"tool_call",...,"tool":"lookup_order","tool_use_id":"call_1","args_hash":"sha256:a6ca6025f652fa70…","elapsed_ms":3}
  {"kind":"tool_call",...,"tool":"lookup_order","tool_use_id":"call_3","args_hash":"sha256:a6ca6025f652fa70…","elapsed_ms":3}
  {"kind":"session_end","session":"support-7f3a","stop_reason":"end_turn","cost_usd":0.0071}

── 3. spans: one tracer, model calls and tool calls ──────────────────────
  agentkit.model_call    stop_reason=tool_use input_tokens=420 output_tokens=60 cost_usd=0.0021
  agentkit.tool_call     tool_name=lookup_order is_error=false
  ...

── 4. the event stream as JSON (session.EventJSON) ───────────────────────
  {"type":"agent_start","session_id":"support-7f3a","provider":"faux","api":"faux","model":"faux-1"}
  {"type":"turn_start","turn_index":0}
  {"type":"tool_call_end","block_index":1,"tool_use_id":"call_1","name":"lookup_order","input":{"id":"A-1001"}}
  ...
```

## The code an application copies

**Hooks.** Every field is optional:

```go
cfg.Hooks = core.Hooks{
    OnSessionStart: func(e core.AuditEvent) { ... },     // once per run
    OnTurnStart:    func(e core.TurnStartEvent) { ... },
    OnTurnEnd:      func(e core.TurnEndEvent) { ... },   // the message, its tool results, its usage
    OnAgentDone:    func(e core.AgentDoneEvent) { ... }, // the RunResult
    OnSessionEnd:   func(e core.AuditEvent) { ... },     // once per run, even on error or abort
    OnError:        func(err error) { ... },             // a panicking hook, an image that would not normalize, ...
    OnAudit:        func(e core.AuditEvent) { ... },     // every audit event, start and end included
}
```

Hooks *observe*. They cannot change the outcome. Each one runs with no agent
lock held and inside a `recover`, so a panic in your metrics code is reported
to `OnError` and the run continues. To *change* what happens, use an
interceptor (`BeforeToolCall`, `AfterToolCall`) or middleware.

**The audit trail.** Register `OnAudit` once and you receive every
`core.AuditEvent`:

| Kind | Carries |
|---|---|
| `session_start` | `SessionID`, `Timestamp` |
| `tool_call` | `ToolName`, `ToolUseID`, `ServerName` (for MCP tools), `ArgumentsHash`, `IsError`, `ErrorCode`, `ElapsedMS` |
| `skills_loaded` | `Skills`, after you call `agent.AuditSkills(names)` |
| `session_end` | `StopReason`, `Usage`, `Error` |

Map it onto your own log schema, as `auditRecord` does, rather than
serializing the struct as it is. `cfg.SessionID` is stamped on every record,
so set it to an id that joins your other logs.

**Arguments are hashed, never logged.** `ArgumentsHash` is
`core.HashArguments(raw)`, a SHA-256 of the argument bytes. Tool arguments
often contain file contents, credentials and personal data, and an audit log
gets shipped to an aggregator and kept for years. The hash lets you correlate
calls (both lookups of `A-1001` above share one) without the audit log
becoming the largest copy of that data.

**Refused calls are audited.** A call that never reached its handler is still
recorded, with an `ErrorCode` saying why: `blocked_by_policy` (your
`BeforeToolCall`), `blocked_by_plugin`, `unknown_tool`, `invalid_arguments`,
`aborted` or `max_tokens`.

**Spans.** Implement `core.Tracer`:

```go
type Tracer interface {
    StartSpan(name string, fn func(Span) error) error
}
```

Pass the same value in two places to get one trace:

```go
cfg.Tracer     = tracer                                          // "agentkit.tool_call" spans
cfg.Middleware = append(cfg.Middleware, middleware.Tracing(tracer)) // "agentkit.model_call" spans
```

A tool call never passes through middleware, which is why tool spans need
their own field. A span lives until `Span.End` is called, **not** until `fn`
returns. A model-call span ends when its response finishes, on another
goroutine. An OpenTelemetry adapter calls its own `End` from `Span.End`.

**Events as JSON.** `agent.Stream` returns every event as it happens.
`session.EventJSON(e)` encodes one as a discriminated union: a `"type"` member
plus exactly the fields that variant carries. Messages inside an event use the
session log's own encoding, so a consumer reading a socket sees the same
message shape it would read back from a log file:

```go
stream, _ := agent.Stream(ctx, prompt)
for e := range stream.Events() {
    b, _ := session.EventJSON(e)
    conn.WriteMessage(b)
}
res, err := stream.RunResult()
```

## Gotchas

- **`OnSessionEnd` fires exactly once per run**, including on an error or an
  abort. A missing `session_end` therefore means the process died, not that
  the run is still going.
- **A refused tool call has an audit record but no span**, because nothing
  ran. Its `ToolExecutionEndEvent` reports `is_error: true`.
- **The turn index starts at 0.** `RunResult.TurnCount` is a count, so it is
  one more than the last index.
- **Deltas are optional.** `text_delta`, `tool_input_delta` and
  `message_update` events are an optimization. A provider that does not
  stream emits none. The `*_end` events always arrive and carry the whole
  value, so a UI can take them as the final state.
- **`session_end.Usage` is the agent's lifetime total.** For one run's usage,
  use `AgentDoneEvent.Result.Usage` or `RunResult.Usage`.
- **Plugins observe through the same points.** An `EventHookPlugin` in
  `cfg.Plugins` receives session start and end alongside your hooks. See
  [`plugins`](../plugins).

## See also

- [`core.Hooks`](../../core/config.go), [`core.AuditEvent`](../../core/audit.go),
  `core.HashArguments`
- [`core.Tracer`, `core.Span`](../../core/trace.go), `middleware.Tracing`
- [`core/event.go`](../../core/event.go): the event types,
  [`session.EventJSON`](../../session/eventjson.go)
- [`streaming`](../streaming): rendering events live in a terminal
- [`middleware`](../middleware): the model-call span and cache attributes
