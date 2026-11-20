package tools

import "github.com/agent-fox-dev/agentkit-go/output"

// Accumulator is the bounded output buffer of package output, under the name
// it has always had here. The two are the same type.
type Accumulator = output.Accumulator

// TruncateMode selects which end of the stream survives truncation. It is
// output.TruncateMode.
type TruncateMode = output.TruncateMode

const (
	// TruncateHead keeps the BEGINNING. Right for a file read or a listing,
	// where the interesting part is at the top.
	TruncateHead = output.TruncateHead
	// TruncateTail keeps the END. Right for `execute`: a failing build puts
	// its error at the end of the log, and head-truncation preserves the
	// banner and discards the failure.
	TruncateTail = output.TruncateTail
	// TruncateMiddle keeps both ends and elides the centre.
	TruncateMiddle = output.TruncateMiddle
)

// NewAccumulator returns an accumulator with the given budget and mode. A
// budget of zero or less means DefaultByteLimit. It is output.NewAccumulator.
func NewAccumulator(capBytes int, mode TruncateMode) *Accumulator {
	return output.NewAccumulator(capBytes, mode)
}
