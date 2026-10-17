package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// What the program keeps of the calls the harness makes: the log of each as it
// happens, and the counts it prints. The calls themselves are served by mcpserve
// and run by the runner; these are the toolset's hooks, called from the runner's
// goroutines.

// hooks are the toolset's own hooks, and the account they keep: the log of
// every call as it happens, and the counts the program prints. Calls arrive
// from several goroutines — a script's nested calls run concurrently — so the
// log is written under a lock and the counts are atomic.
//
// The log is what the hooks see. The guard wrapper sees a call before it runs
// (run, blocked), After sees one that ran (error). A call that never reaches
// the guard or After — an unknown tool, arguments that fail validation — is
// counted and has no line.
type hooks struct {
	w              io.Writer
	mu             sync.Mutex
	calls, blocked atomic.Int64
}

func (h *hooks) printf(format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fmt.Fprintf(h.w, format, args...)
}

// indentFor nests a script's calls under the code_mode call.
func indentFor(parentID string) string {
	if parentID != "" {
		return "    "
	}
	return "  "
}

// guard decides with configured, then logs the call as run or blocked and
// counts a block.
func (h *hooks) guard(configured core.BeforeToolCall) core.BeforeToolCall {
	return func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		dec := configured(ctx, in)
		indent := indentFor(in.ParentToolUseID)
		if dec.Block {
			h.blocked.Add(1)
			reason := dec.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			h.printf("%s%-8s %s: %s\n", indent, "blocked", in.ToolName, firstLine(reason))
			return dec
		}
		h.printf("%s%-8s %s %s\n", indent, "run", in.ToolName, summarize(in.Arguments))
		return dec
	}
}

// after logs a call that ran and failed.
func (h *hooks) after(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
	if res := in.ToolResult; !res.OK {
		detail := res.Detail
		if code, ok := res.Data["exit_code"]; ok && detail == "" {
			detail = fmt.Sprintf("exit %v", code)
		}
		h.printf("%s%-8s %s: %s\n", indentFor(in.ParentToolUseID), "error", in.ToolName, firstLine(res.Error+": "+detail))
	}
	return core.AfterToolCallDecision{}
}

// event counts every call: each one, direct or nested, opens with a start
// event.
func (h *hooks) event(e core.Event) {
	if _, ok := e.(core.ToolExecutionStartEvent); ok {
		h.calls.Add(1)
	}
}

// errShellBlocked is what a check whose command the guard refused reports.
var errShellBlocked = errors.New("the guard refused the command")

// summarize renders tool arguments as one short line, keys sorted.
func summarize(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(firstLine(fmt.Sprint(args[k])), 60)))
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return truncate(s, 120)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
