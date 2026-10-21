package openrouter

import (
	"context"
	"errors"
	"fmt"
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

// transport is the HTTPClient the SDK sends through.
type transport struct{ inner *http.Client }

// Do sends the request.
func (t transport) Do(req *http.Request) (*http.Response, error) { return t.inner.Do(req) }

// Stream is not implemented yet.
func (c *client) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	return nil, errors.New("openrouter: Stream is not implemented")
}
