package codemode

import (
	"encoding/json"
	"os"
	"time"

	"go.starlark.net/starlark"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// newOutput is a script's output buffer: the same bounded accumulator the
// shell tools use, keeping the head and tail of MaxOutputBytes and, unless
// DisableSpill, writing everything to a file in SpillDir (08-REQ-8.1).
func newOutput(opts Options) *tools.Accumulator {
	acc := tools.NewAccumulator(opts.MaxOutputBytes, tools.TruncateMiddle)
	if !opts.DisableSpill {
		acc.SpillDir = opts.SpillDir
		acc.SpillPrefix = "codemode"
	}
	return acc
}

// finishOutput closes the output and records it in the result's metadata.
// The accumulator writes its spill file from the first byte, so a run whose
// output fit removes the file: only truncated output is spilled, as
// 08-REQ-8.4 asks.
func (r *runner) finishOutput(res *core.ToolResult, elapsed time.Duration) {
	_ = r.out.Close()
	md := &core.ToolMetadata{DurationMS: elapsed.Milliseconds()}
	if r.out.Truncated() {
		md.Truncated = true
		md.TruncatedBy = string(tools.TruncatedByBytes)
		md.TotalBytes = r.out.Total()
		md.SpillPath = r.out.SpillPath()
	} else if p := r.out.SpillPath(); p != "" {
		_ = os.Remove(p)
	}
	res.Metadata = md
}

// FormatResultText is the text the model reads for a script that finished:
// what it printed, then "Return value: <value>" when it returned one, or a
// note that there was neither (08-REQ-8.3). A Starlark value is rendered as
// Starlark writes it; any other value as JSON.
func FormatResultText(printed string, returnValue any) string {
	text := printed
	if line := returnLine(returnValue); line != "" {
		if text != "" {
			text += "\n"
		}
		text += line
	}
	if text == "" {
		return "[Script finished with no output]"
	}
	return text
}

// returnLine renders a return value, or "" for none.
func returnLine(v any) string {
	switch x := v.(type) {
	case nil, starlark.NoneType:
		return ""
	case starlark.Value:
		return "Return value: " + x.String()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return "Return value: " + string(b)
}
