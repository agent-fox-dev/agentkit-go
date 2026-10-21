package ollama

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Options configure one provider. The zero value reads the process
// environment and uses the defaults.
type Options struct {
	// Host is the server's address. It overrides HostVar, and when it is set the
	// provider reads no variable at all.
	Host string
	// Env holds explicit values for HostVar. A variable is read from Env first;
	// an empty value falls through to Getenv.
	Env map[string]string
	// Getenv looks a variable up when Env has no value for it. Nil means the
	// process environment. The provider asks for HostVar and for nothing else.
	Getenv func(string) string
	// HTTPClient sends the requests. Nil means a client with the default
	// transport and no overall timeout, so a long stream is cut only by the
	// caller's context. Its Transport runs beneath the layer that deletes the
	// Authorization header, and may add headers of its own.
	HTTPClient *http.Client
	// Now is the clock for message timestamps. Nil means time.Now.
	Now func() time.Time
	// Warnf receives the report of a transcript repair. Nil discards it.
	Warnf func(format string, args ...any)
}

// env resolves the variable the provider reads: Options.Env first, then
// Options.Getenv (the process environment when nil). An empty value is unset.
type env struct {
	set    map[string]string
	getenv func(string) string
}

// lookup returns the value of name, or "" when it is unset or empty. It is only
// ever called with HostVar.
func (e env) lookup(name string) string {
	if v := e.set[name]; v != "" {
		return v
	}
	if e.getenv != nil {
		return e.getenv(name)
	}
	return os.Getenv(name)
}

// Client is the provider for one model. Nothing in it changes after New, so
// Stream is safe for concurrent use.
type Client struct {
	model core.Model
	base  *url.URL
	// api is the SDK client every request goes through: api.NewClient with the
	// credential-free transport, never api.ClientFromEnvironment, which reads
	// the process environment.
	api   *api.Client
	ids   *callIDs
	now   func() time.Time
	warnf func(format string, args ...any)
}

// New builds a provider for one model. It resolves the server's address from
// opts and the environment and makes no request: an unreachable server is found
// on the first Stream.
//
// New fails, before any request, when m.ID is empty after trimming, and when the
// host is not an address the provider accepts (the error wraps ErrInvalidHost).
func New(m core.Model, opts Options) (*Client, error) {
	if strings.TrimSpace(m.ID) == "" {
		return nil, errors.New("ollama: the model id is empty")
	}
	raw := opts.Host
	if strings.TrimSpace(raw) == "" {
		raw = env{set: opts.Env, getenv: opts.Getenv}.lookup(HostVar)
	}
	base, err := parseHost(raw)
	if err != nil {
		return nil, err
	}
	c := &Client{model: m, base: base, ids: newCallIDs(), now: opts.Now, warnf: opts.Warnf}
	c.api = api.NewClient(base, sdkHTTPClient(opts.HTTPClient))
	if c.now == nil {
		c.now = time.Now
	}
	if c.warnf == nil {
		c.warnf = func(string, ...any) {}
	}
	return c, nil
}

// Host returns the resolved base URL of the server, for example
// "http://127.0.0.1:11434".
func (c *Client) Host() string { return c.base.String() }
