package openrouter

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	sdk "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/retry"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// client is the provider for one model. Nothing in it changes after New, so
// Stream is safe for concurrent use.
type client struct {
	model      core.Model
	sdk        *sdk.OpenRouter
	key        string
	maxRetries int
	now        func() time.Time
	warnf      func(format string, args ...any)
}

// New builds a provider for one model. It resolves the API key and the base
// URL from opts and the environment, builds the one SDK client every request
// goes through, and makes no request: an unreachable host is found on the
// first Stream.
//
// New fails, before any request, when m.ID is empty, when OPENROUTER_API_KEY
// is unset (the error wraps ErrNoAPIKey), or when OPENROUTER_BASE_URL is set
// but is not an absolute http or https URL.
func New(m core.Model, opts Options) (core.ProviderClient, error) {
	if m.ID == "" {
		return nil, errors.New("openrouter: the model id is empty")
	}
	e := env{set: opts.Env, getenv: opts.Getenv}
	key := e.lookup(APIKeyVar)
	if key == "" {
		return nil, fmt.Errorf("%w (set it in the environment or in Options.Env)", ErrNoAPIKey)
	}
	base := DefaultBaseURL
	if raw := e.lookup(BaseURLVar); raw != "" {
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("openrouter: %s is %q, not an absolute http or https URL", BaseURLVar, withoutUserInfo(raw))
		}
		base = strings.TrimRight(raw, "/")
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		// The SDK's own default client times a request out after a minute,
		// which would cut a long stream.
		httpClient = &http.Client{}
	}
	c := &client{
		model:      m,
		key:        key,
		maxRetries: DefaultMaxRetries,
		now:        opts.Now,
		warnf:      opts.Warnf,
	}
	if opts.MaxRetries != nil {
		c.maxRetries = max(*opts.MaxRetries, 0)
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.warnf == nil {
		c.warnf = func(string, ...any) {}
	}
	c.sdk = sdk.New(
		sdk.WithSecurity(key),
		sdk.WithServerURL(base),
		sdk.WithClient(transport{inner: httpClient}),
		// The provider runs its own retry loop; the SDK's retries only 5xx and
		// can back off for an hour.
		sdk.WithRetryConfig(retry.Config{Strategy: "none"}),
		// The SDK reads OPENROUTER_HTTP_REFERER and OPENROUTER_X_TITLE unless
		// they are set. It never sends them for a chat completion, but the
		// provider reads no variable but its two, so they are set here.
		sdk.WithHTTPReferer(""),
		sdk.WithXTitle(""),
	)
	return c, nil
}

// userInfo matches the user-info part of a URL with a scheme.
var userInfo = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]*://)[^/?#]*@`)

// withoutUserInfo returns raw with any user:password@ removed, so a base URL
// can be shown in an error.
func withoutUserInfo(raw string) string { return userInfo.ReplaceAllString(raw, "$1") }

// attempt is the side channel of one SDK request: the transport reads from it
// the body to send, and the SDK never sees that body.
type attempt struct{ body []byte }

type attemptKey struct{}

// transport is the HTTPClient the SDK sends through.
//
// AgentKit writes the request body itself, because the SDK's request types
// would re-encode it: a tool's parameters are a map, which loses property
// order, and reasoning_details is a union that drops fields it does not model.
// The SDK is given a placeholder request, and the transport replaces the body
// it serialized with the one the request's context carries
// (docs/errata/27_openrouter_provider.md).
type transport struct{ inner *http.Client }

// Do sends the request, with the body of the attempt its context carries when
// there is one.
func (t transport) Do(req *http.Request) (*http.Response, error) {
	if a, ok := req.Context().Value(attemptKey{}).(*attempt); ok {
		req = req.Clone(req.Context())
		req.Body = io.NopCloser(bytes.NewReader(a.body))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(a.body)), nil }
		req.ContentLength = int64(len(a.body))
	}
	return t.inner.Do(req)
}
