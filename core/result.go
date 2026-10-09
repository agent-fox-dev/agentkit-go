package core

// RunResult is the flattened ACTIVE BRANCH — a view, not the durable
// representation, and not what a caller should persist (§5, NFR-REL-04).
type RunResult struct {
	Messages   Messages
	StopReason RunStopReason
	// LastReason is the final assistant message's canonical reason, kept
	// because RunStopReason and StopReason are different vocabularies.
	LastReason StopReason
	Usage      Usage
	TurnCount  int
	Error      error
}

func (r RunResult) FinalText() string {
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if a, ok := r.Messages[i].(AssistantMessage); ok {
			return a.Content.Text()
		}
	}
	return ""
}
