# Erratum: spec 09 `repository_cut`

Where the delivered cut differs from `.specs/09_repository_cut`, and why.

## Design decision 4: the Anthropic provider needed more than updated imports

**Spec.** `provider/credentials.go`, `provider/repair.go` and
`provider/salvage.go` are deleted (09-REQ-1.2), and the existing Anthropic
provider is "kept operational with its imports updated".

**Code.** The Anthropic provider called into all three files, so updated
imports alone could not keep it building. The parts it runs on every request
moved into the provider package itself:
`RepairTranscript` (`provider/anthropic/repair.go:118`), which among other
rules drops an aborted or errored turn from the outbound request, and
`SalvageJSON` (`provider/anthropic/salvage.go:32`), which closes truncated
tool arguments. Their tests moved with them (`provider/anthropic/repair_test.go`,
`provider/anthropic/salvage_test.go`). The application-owned credential store
(`anthropic.Options.Credentials`, `provider.Credentials`, OAuth refresh) had no
consumer and was removed. The provider now authenticates from the environment
table only (`provider/anthropic/stream.go:268`). TS-09-1 checks that the three
files are gone from `provider/`. Spec 10 replaces all of this with the
official SDK.

## 09-REQ-5.1: there was no `middleware.Chain` field

**Spec.** Remove the `chain middleware.Chain` field and the `Use` method from
`Agent`.

**Code.** `Agent` had neither. It held a `*middleware.CacheMeter` (with the
`CacheStats` and `Meter` methods), and that is what was removed. The
model-call middleware seam is `core.AgentConfig.Middleware`
(`core/config.go:100`), which depends on nothing in the deleted package. It
stays until spec 11 reshapes the config. TS-09-10 checks the fields and
methods.

## 09-REQ-5.2: the context estimate is no longer sent

**Spec.** Remove the call to `compaction.EstimateContextTokens`.

**Code.** Removed. `core.Request.EstContextTokens` is therefore zero, and the
Anthropic provider's `max_tokens` clamp (`provider/anthropic/anthropic.go:274`)
now clamps to the model's output cap only, not to what is left of the context
window. Spec 10's SDK-based wire owns the clamp from here on.

## 09-REQ-5.3 / TS-09-11: the Anthropic API id

**Spec.** The registry is looked up as `Get("anthropic")`.

**Code.** The Anthropic provider registers under `anthropic.API`, which is
`"anthropic-messages"` (`provider/anthropic/anthropic.go:17`). TS-09-11 looks
it up by that constant.

## 09-REQ-2.1: the MCP server's other halves

**Spec.** `mcp/server.go` and `mcp/serve.go` are deleted.

**Code.** Deleted, along with the `[mcp_server]` configuration section that
only configured that server (`mcp.Config` now holds only `Servers`,
`mcp/config.go:16`) and `mcp.DefaultLimits` (callers use `wire.Defaults()`).
The MCP client tests used the deleted server as their in-process peer. They
now connect to the official SDK's server through a small test fixture
(`mcp/mcp_test.go:36`), and so do `examples/mcp` and `examples/codemode`.
Removing `core/audit.go` also removed the MCP client's `Audit` and `Now`
connection options.

## 09-REQ-2.2: image blocks are no longer normalized

**Spec.** `images.go` is deleted.

**Code.** Deleted. It re-encoded every image block in a tool result to the
provider's limits at the history boundary. An image block from an MCP server or
a custom tool now reaches the transcript as the tool produced it.

## Spec 08 and spec 07 tests that asserted through deleted hooks

The code mode smoke tests (TS-08-45, -46, -48), the nested-call smoke test
(TS-07-35) and TS-07-22 counted nested calls through the audit hook or the
tracer. They now assert the same linkage on the event stream:
`ToolExecutionEndEvent.ParentToolUseID`. TS-07-30 (nested audit) and TS-07-31
(nested tracing) tested the deleted hooks themselves and were removed, as the
source PRD (§8) directs.

## `core.MarshalEvent` has no shipped message encoder

`core.MarshalEvent` (`core/eventjson.go:22`) takes a `core.MessageEncoder`.
The only one the module shipped was the session log's codec, which went with
`session`. A caller that serializes message-bearing events now supplies its
own encoder. Spec 12 reshapes `core`.
