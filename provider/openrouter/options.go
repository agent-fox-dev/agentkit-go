package openrouter

import (
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

const (
	// API is the wire API this provider speaks: OpenRouter's native Chat
	// Completions. It is the api string of the catalog's openrouter vendor.
	API core.API = "openrouter-chat"

	// APIKeyVar names the environment variable that holds the API key.
	APIKeyVar = "OPENROUTER_API_KEY"
	// BaseURLVar names the environment variable that overrides the base URL.
	BaseURLVar = "OPENROUTER_BASE_URL"

	// DefaultBaseURL is the base URL when BaseURLVar is unset. Every request is
	// a POST to <base>/chat/completions.
	DefaultBaseURL = "https://openrouter.ai/api/v1"

	// DefaultMaxRetries is how many times a request that failed with a 429 or
	// a 5xx is sent again when Options.MaxRetries is nil.
	DefaultMaxRetries = 2
)

// ErrNoAPIKey is the error New wraps when OPENROUTER_API_KEY resolves to
// nothing. Its text names the variable.
var ErrNoAPIKey = errors.New("openrouter: " + APIKeyVar + " is not set")

// Options configure one provider. The zero value reads the process
// environment and uses the defaults.
type Options struct {
	// Env holds explicit values for APIKeyVar and BaseURLVar. A variable is
	// read from Env first; an empty value falls through to Getenv.
	Env map[string]string
	// Getenv looks a variable up when Env has no value for it. Nil means the
	// process environment. The provider asks for APIKeyVar and BaseURLVar and
	// for nothing else.
	Getenv func(string) string
	// HTTPClient sends the requests. Nil means a client with no overall
	// timeout, so a long stream is cut only by the caller's context.
	HTTPClient *http.Client
	// MaxRetries is how many times a request is sent again after a 429 or a
	// 5xx. Nil means DefaultMaxRetries; a negative value means none.
	MaxRetries *int
	// Now is the clock for message timestamps and for a Retry-After given as a
	// date. Nil means time.Now.
	Now func() time.Time
	// Warnf receives a warning about a repair the provider made or an input it
	// could not use. Nil discards warnings.
	Warnf func(format string, args ...any)
}

// env resolves the variables the provider reads: Options.Env first, then
// Options.Getenv (the process environment when nil). An empty value is unset.
type env struct {
	set    map[string]string
	getenv func(string) string
}

// lookup returns the value of name, or "" when it is unset or empty. It is
// only ever called with APIKeyVar and BaseURLVar.
func (e env) lookup(name string) string {
	if v := e.set[name]; v != "" {
		return v
	}
	if e.getenv != nil {
		return e.getenv(name)
	}
	return os.Getenv(name)
}
