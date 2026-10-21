package ollama

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/ollama/ollama/api"
)

// failure is an error with a text of its own that wraps the errors it stands
// for, so a caller can ask errors.Is for the sentinel and errors.As for the
// *StatusError or the connection error behind it.
type failure struct {
	text string
	errs []error
}

func (f *failure) Error() string   { return f.text }
func (f *failure) Unwrap() []error { return f.errs }

// failureOf says why a Chat call ended without a done chunk, and the caller has
// not cancelled it. Chat returns:
//
//   - an api.StatusError for an error status whose body has a line, and an
//     api.AuthorizationError for a 401 with one;
//   - nil for an error status whose body is empty, which only the status the
//     transport recorded in a tells from a stream that was cut (a 2xx status,
//     or none, is the cut);
//   - a *net.OpError whose Op is "dial" for a failure to connect;
//   - a plain error for an error line inside a 200 stream, and for anything else.
//
// Every text starts with "ollama:". None holds the value of an environment
// variable: the provider reads only OLLAMA_HOST, and the host is shown.
func (c *Client) failureOf(a *attempt, err error) error {
	var status api.StatusError
	var auth api.AuthorizationError
	var op *net.OpError
	switch {
	case errors.As(err, &status):
		return c.statusFailure(status.StatusCode, status.ErrorMessage)
	case errors.As(err, &auth):
		return c.statusFailure(auth.StatusCode, "")
	case err == nil && a.status >= http.StatusBadRequest:
		return c.statusFailure(a.status, "")
	case err == nil:
		return ErrStreamTruncated
	case errors.As(err, &op) && op.Op == "dial":
		return c.unreachable(op)
	}
	if msg := err.Error(); isContextWindow(msg) {
		return c.contextWindow(0, msg)
	}
	return &failure{text: "ollama: " + err.Error(), errs: []error{err}}
}

// statusFailure is the error for an HTTP error status and Ollama's message, or
// the status text when it gave none.
func (c *Client) statusFailure(code int, msg string) error {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = http.StatusText(code)
	}
	switch {
	case code == http.StatusNotFound:
		text := fmt.Sprintf("ollama: HTTP 404: the model %q is not on the server at %s (Ollama said: %s); run \"ollama pull %s\"",
			c.model.ID, c.Host(), msg, c.model.ID)
		return &failure{text: text, errs: []error{ErrModelNotFound, &StatusError{Code: code, Text: text}}}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &StatusError{Code: code, Text: fmt.Sprintf(
			"ollama: HTTP %d from %s: AgentKit sends no credential to Ollama; check %s (Ollama said: %s)", code, c.Host(), HostVar, msg)}
	case isContextWindow(msg):
		return c.contextWindow(code, msg)
	}
	return &StatusError{Code: code, Text: fmt.Sprintf("ollama: HTTP %d: %s", code, msg)}
}

// isContextWindow reports whether Ollama's message says the input exceeds the
// context length: "the input length exceeds the context length", "input after
// truncation exceeds maximum context length" and the like.
func isContextWindow(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "context length") && strings.Contains(m, "exceed")
}

// contextWindow is the error for an input longer than the context length. code is
// the HTTP status it came with, or 0 for an error line inside a 200 stream. The
// text says that num_ctx takes effect only when a model loads, so a model already
// loaded at a smaller length keeps it.
func (c *Client) contextWindow(code int, msg string) error {
	text := fmt.Sprintf("ollama: %s (model %q, num_ctx %d sent); shorten the input or ask for a larger context length, "+
		"but a model already loaded at a smaller length keeps it until it unloads", msg, c.model.ID, c.contextLength())
	errs := []error{ErrContextWindow}
	if code != 0 {
		errs = append(errs, &StatusError{Code: code, Text: text})
	}
	return &failure{text: text, errs: errs}
}

// unreachable is the error for a failure to connect: a refused connection, a
// name that does not resolve, no route.
func (c *Client) unreachable(op *net.OpError) error {
	text := fmt.Sprintf("ollama: cannot reach the Ollama server at %s (%v); start it with \"ollama serve\", or set %s to its address",
		c.Host(), op, HostVar)
	return &failure{text: text, errs: []error{ErrUnreachable, op}}
}
