package codemode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/agentfox/agentkit-go/core"
)

// ledgerEntry is one nested call a script made and how it ended. Its side
// effects are not undone when the script fails later, so the model needs to
// know it happened (08-REQ-9.3).
type ledgerEntry struct {
	tool  string
	args  map[string]any
	ok    bool
	error string
}

// record adds the calls of one dispatch to the ledger.
func (r *runner) record(blocks []core.ToolUseBlock, res []core.ToolResult) {
	for i, b := range blocks {
		var args map[string]any
		_ = json.Unmarshal(b.Input, &args)
		if args == nil {
			args = map[string]any{}
		}
		e := ledgerEntry{tool: b.Name, args: args, ok: res[i].OK}
		if !res[i].OK {
			e.error = res[i].Error
		}
		r.ledger = append(r.ledger, e)
	}
}

// ledgerData is the ledger as Data's calls_completed.
func (r *runner) ledgerData() []any {
	out := make([]any, len(r.ledger))
	for i, e := range r.ledger {
		m := map[string]any{"tool": e.tool, "arguments": e.args, "ok": e.ok}
		if !e.ok {
			m["error"] = e.error
		}
		out[i] = m
	}
	return out
}

// ledgerText is the ledger as the model reads it: one line per call,
// tool(arg=value, ...) and its outcome.
func (r *runner) ledgerText() string {
	if len(r.ledger) == 0 {
		return "Calls completed: none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Calls completed (%d):", len(r.ledger))
	for _, e := range r.ledger {
		status := "ok"
		if !e.ok {
			status = "failed: " + e.error
		}
		fmt.Fprintf(&b, "\n- %s(%s) %s", e.tool, renderArgs(e.args), status)
	}
	return b.String()
}

// renderArgs is a call's arguments as keyword arguments, sorted by name,
// each value as JSON.
func renderArgs(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		v, _ := json.Marshal(args[k])
		parts[i] = k + "=" + string(v)
	}
	return strings.Join(parts, ", ")
}
