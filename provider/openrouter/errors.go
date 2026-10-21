package openrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

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

// httpError is the StatusError for a response that was not 2xx: the status, and
// the vendor's message when the body has one. A 401 or a 403 also says which
// variable to check. The text never holds the API key.
func (c *client) httpError(status int, body []byte) *StatusError {
	text := fmt.Sprintf("openrouter: HTTP %d", status)
	msg := vendorMessage(body)
	if msg == "" {
		msg = http.StatusText(status)
	}
	if msg != "" {
		text += ": " + msg
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		text += " (check " + APIKeyVar + ")"
	}
	return &StatusError{Code: status, Text: c.redact(text)}
}

// vendorMessage is error.message of an OpenRouter error body. A body that is
// not JSON is its own message, cut to its first line; a JSON body without a
// message has none.
func vendorMessage(body []byte) string {
	if !json.Valid(body) {
		line, _, _ := strings.Cut(strings.TrimSpace(string(body)), "\n")
		if r := []rune(line); len(r) > maxMessageRunes {
			line = string(r[:maxMessageRunes]) + "..."
		}
		return strings.TrimSpace(line)
	}
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &env) != nil || len(env.Error) == 0 {
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(env.Error, &obj) == nil && strings.TrimSpace(obj.Message) != "" {
		return strings.TrimSpace(obj.Message)
	}
	var str string
	if json.Unmarshal(env.Error, &str) == nil {
		return strings.TrimSpace(str)
	}
	return ""
}

// maxMessageRunes bounds the part of a body that is not JSON shown in an error.
const maxMessageRunes = 200

// redacted is an error whose text has had the API key replaced; it still wraps
// the error it stands for, so errors.Is and errors.As see through it.
type redacted struct {
	msg string
	err error
}

func (e *redacted) Error() string { return e.msg }
func (e *redacted) Unwrap() error { return e.err }
