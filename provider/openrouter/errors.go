package openrouter

import "errors"

// ErrStreamTruncated is a response body that ended before any choice carried a
// finish_reason. A Chat Completions tool call is only known to be complete at
// its finish_reason, so such a turn fails and keeps no tool call.
var ErrStreamTruncated = errors.New("openrouter: stream ended before finish_reason")

// StatusError is an error OpenRouter reported, with its code: the HTTP status,
// or the code of an error chunk inside a stream that answered 200. A caller's
// retry policy that matches "429" or "503" finds it in Text as well.
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string { return e.Text }
