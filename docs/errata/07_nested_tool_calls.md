# Erratum: spec 07 `nested_tool_calls`

Where the delivered nested tool calls differ from `.specs/07_nested_tool_calls`,
and why.

## 07-REQ-8.2: nested calls emit start and end events, not update events

**Spec.** Nested calls emit `ToolExecutionStartEvent`,
`ToolExecutionUpdateEvent` and `ToolExecutionEndEvent` with `ParentToolUseID`.

**Code.** Nothing in production emits `ToolExecutionUpdateEvent`, for direct
calls or nested ones: no tool streams partial output through the loop. The
event gains `ParentToolUseID` (`core/event.go`), encoded as
`parent_tool_use_id` with `omitempty` (`core/eventjson.go`). Nested calls emit
start and end events (`nested.go`). Test: TS-07-29,
`TestNestedEvents_TS07_29`, which checks the update event's encoding with and
without a parent.

## 07-REQ-5.3, 07-REQ-6: `AfterToolCall` on a nested call

**Spec.** An interceptor's terminate vote ends the wrapper, and the spec's
example is `BeforeToolCall`'s `Block` with `Terminate`.

**Code.** `AfterToolCall` returning `Terminate: true` for a nested call ends
the wrapper the same way. PRD goal 9 gives "an interceptor's terminate vote"
this authority, and the after-interceptor is as much the embedder's as the
before one. `AfterToolCall` may still edit the result message it is handed,
but the wrapper receives a `core.ToolResult`, so those edits do not reach it.

## Test-spec signatures that do not exist

- `NewAgent(cfg, tools...)` is `NewAgent(cfg)`. Tools reach an agent through
  `AgentConfig.ToolPolicy.CustomTools` or `RegisterTool`.
- Interceptors return a decision, not a decision and an error.
  `BeforeToolCallDecision.Arguments` is a `map[string]any`.
- `core.ToolResult` has no tool-use id. TS-07-25 checks result order through
  each result's `Data`.
- `checkExecuteGuard` is a method on `*Agent`, not a function taking a config
  and tools.
