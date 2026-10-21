package provider

import (
	"fmt"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// SyntheticResultText is the content of the tool result RepairToolPairing
// invents for a call nothing answered. It is model-visible, so it is pinned
// here rather than formatted at the call site.
const SyntheticResultText = "No result provided"

// PairingReport records what RepairToolPairing changed. It is returned rather
// than logged, so a caller diagnosing a 400 can tell whether the transcript it
// sent is the transcript it holds.
type PairingReport struct {
	// DroppedAssistants counts assistant messages that ended in an error or
	// were aborted.
	DroppedAssistants int
	// DroppedResults counts tool results that answered no call of the turn
	// they sit in: the results of a dropped turn, results of an unknown call,
	// and a second result for one call.
	DroppedResults int
	// Inserted counts the synthetic results added for unanswered calls.
	Inserted int
}

// Changed reports whether the repair altered the transcript.
func (r PairingReport) Changed() bool {
	return r.DroppedAssistants+r.DroppedResults+r.Inserted > 0
}

func (r PairingReport) String() string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(r.DroppedAssistants, "failed turns dropped")
	add(r.DroppedResults, "orphaned results dropped")
	add(r.Inserted, "synthetic results inserted")
	if len(parts) == 0 {
		return "no repairs"
	}
	return strings.Join(parts, ", ")
}

// RepairToolPairing is the transcript repair shared by the chat-style wires
// (OpenRouter and Ollama Chat Completions): wires that pair a tool call with
// its result by id in separate messages, and reject a call that has none.
//
// It returns a repaired view and never changes its input, its messages or
// their content. It applies three rules:
//
//  1. An assistant message that ended in an error or was aborted is dropped.
//  2. A tool result is kept only in the run of results that directly follows
//     the assistant message that made the call. A result of a dropped turn, of
//     an unknown call, one that follows no assistant message, and a second
//     result for the same call are dropped.
//  3. A call of a kept assistant message with no result in its run gets an
//     error result with the text SyntheticResultText. The run is then ordered
//     as the calls are, so a wire that pairs by position pairs correctly; a
//     run that needs no synthetic result is left as it was recorded.
//
// The result is a fixed point: repairing it again changes nothing. Unlike the
// Anthropic repair it never touches thinking, which a chat-style wire replays
// on its own terms.
func RepairToolPairing(in core.Messages) (core.Messages, PairingReport) {
	var rep PairingReport
	out := make(core.Messages, 0, len(in))
	for i := 0; i < len(in); {
		switch m := in[i].(type) {
		case core.AssistantMessage:
			if m.StopReason.ShortCircuits() {
				rep.DroppedAssistants++
				i++
				continue
			}
			j := i + 1
			for j < len(in) {
				if _, ok := in[j].(core.ToolResultMessage); !ok {
					break
				}
				j++
			}
			out = append(out, m)
			out = append(out, repairRun(m, in[i+1:j], &rep)...)
			i = j
		case core.ToolResultMessage:
			// A result that no kept assistant message precedes directly.
			rep.DroppedResults++
			i++
		default:
			out = append(out, in[i])
			i++
		}
	}
	return out, rep
}

// repairRun returns the results that follow the kept assistant message am,
// repaired under rules 2 and 3. run holds only tool results.
func repairRun(am core.AssistantMessage, run core.Messages, rep *PairingReport) core.Messages {
	calls := core.ExtractToolUse(&am)
	idx := make(map[string]int, len(calls)) // call id -> index of its first result in run
	extras := make([]bool, len(run))
	for k, m := range run {
		id := m.(core.ToolResultMessage).ToolUseID
		if _, dup := idx[id]; dup {
			extras[k] = true
			continue
		}
		idx[id] = k
	}
	isCall := make(map[string]bool, len(calls))
	for _, c := range calls {
		isCall[c.ID] = true
	}
	for k, m := range run {
		if !isCall[m.(core.ToolResultMessage).ToolUseID] {
			extras[k] = true
		}
	}
	missing := false
	for _, c := range calls {
		if _, ok := idx[c.ID]; !ok {
			missing = true
			break
		}
	}

	if !missing {
		// Keep the run in its recorded order, minus the results that answer
		// nothing.
		kept := make(core.Messages, 0, len(run))
		for k, m := range run {
			if extras[k] {
				rep.DroppedResults++
				continue
			}
			kept = append(kept, m)
		}
		return kept
	}

	// Rebuild the run in call order: each call's own result, or a synthetic one.
	rebuilt := make(core.Messages, 0, len(calls))
	done := make(map[string]bool, len(calls))
	for _, c := range calls {
		if done[c.ID] {
			continue
		}
		done[c.ID] = true
		if k, ok := idx[c.ID]; ok {
			rebuilt = append(rebuilt, run[k])
			continue
		}
		rep.Inserted++
		rebuilt = append(rebuilt, core.ToolResultMessage{
			ToolUseID: c.ID,
			ToolName:  c.Name,
			Content:   core.Content{core.TextBlock{Text: SyntheticResultText}},
			IsError:   true,
		})
	}
	for _, drop := range extras {
		if drop {
			rep.DroppedResults++
		}
	}
	return rebuilt
}
