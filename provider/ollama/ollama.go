// Package ollama is a core.ProviderClient for a model served by an Ollama
// server, local or self-hosted. It reaches the server through Ollama's own Go
// package, github.com/ollama/ollama/api, and Ollama's native /api/chat
// endpoint, and owns what that package does not: turning a canonical request
// into an Ollama chat request, streaming the response back as canonical events
// and an assistant message, and handling the context window, thinking, tool
// calls, errors and usage the way Ollama documents them.
//
// The provider sends no credential and reads one variable, OLLAMA_HOST. It
// reads no model metadata of its own: the window, the output cap and the
// efforts a model takes come from the core.Model passed to New, which package
// catalog supplies.
package ollama

import (
	"errors"

	"github.com/agent-fox-dev/agentkit-go/core"
)

const (
	// API is the wire API this provider speaks: Ollama's native chat. It is the
	// api string of the catalog's ollama vendor.
	API core.API = core.APIOllamaChat

	// HostVar names the environment variable that holds the server's address.
	// It is the only variable the provider reads.
	HostVar = "OLLAMA_HOST"

	// DefaultHost is the address when HostVar is empty or unset. Every request
	// is a POST to <host>/api/chat.
	DefaultHost = "http://127.0.0.1:11434"
)

// The errors a turn can end with, each wrapped by an error whose text names
// what to do about it.
var (
	// ErrInvalidHost is the error New wraps when the host is not an address the
	// provider accepts.
	ErrInvalidHost = errors.New("ollama: invalid host")
	// ErrStreamTruncated is a response that ended before the chunk with
	// done: true. Such a turn fails and keeps no tool call.
	ErrStreamTruncated = errors.New("ollama: stream ended before done")
	// ErrModelNotFound is a 404: the server does not have the model.
	ErrModelNotFound = errors.New("ollama: model not found")
	// ErrUnreachable is a failure to connect to the server.
	ErrUnreachable = errors.New("ollama: server unreachable")
	// ErrContextWindow is Ollama refusing an input longer than the context
	// length the model runs at.
	ErrContextWindow = errors.New("ollama: input exceeds the context length")
)

// StatusError is an error Ollama reported, with its code: the HTTP status.
// A retry policy that matches "429" or "503" finds it in Text as well.
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string { return e.Text }
