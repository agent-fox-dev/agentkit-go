package openrouter

import (
	"bytes"
	"context"
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
	redactor   *strings.Replacer
	maxRetries int
	now        func() time.Time
	warnf      func(format string, args ...any)
	// sleep waits between attempts; a test replaces it.
	sleep func(ctx context.Context, d time.Duration) error
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
	redactor := newRedactor(key)
	base := DefaultBaseURL
	if raw := e.lookup(BaseURLVar); raw != "" {
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("openrouter: %s is %q, not an absolute http or https URL", BaseURLVar,
				redactor.Replace(withoutUserInfo(raw)))
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
		redactor:   redactor,
		maxRetries: DefaultMaxRetries,
		now:        opts.Now,
	}
	if opts.MaxRetries != nil {
		c.maxRetries = max(*opts.MaxRetries, 0)
	}
	if c.now == nil {
		c.now = time.Now
	}
	// Every warning has the key taken out of it before it reaches Options.Warnf.
	c.warnf = func(string, ...any) {}
	if warn := opts.Warnf; warn != nil {
		c.warnf = func(format string, args ...any) { warn("%s", c.redact(fmt.Sprintf(format, args...))) }
	}
	c.sleep = sleepCtx
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

// attempt is the side channel of one SDK request. The transport reads from it
// the body to send, which the SDK never sees, and records in it what the SDK's
// typed errors leave out: the response's status and Retry-After header, and the
// body of an error response.
type attempt struct {
	body []byte

	status     int
	retryAfter string
	errBody    []byte
}

// maxErrorBody bounds how much of an error response the transport keeps.
const maxErrorBody = 1 << 20

type attemptKey struct{}

// transport is the HTTPClient the SDK sends through.
//
// AgentKit writes the request body itself, because the SDK's request types
// would re-encode it: a tool's parameters are a map, which loses property
// order, and reasoning_details is a union that drops fields it does not model.
// The SDK is given a placeholder request, and the transport replaces the body
// it serialized with the one the request's context carries
// (docs/adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md).
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
	resp, err := t.inner.Do(req)
	if a, ok := req.Context().Value(attemptKey{}).(*attempt); ok && err == nil {
		a.status = resp.StatusCode
		a.retryAfter = resp.Header.Get("Retry-After")
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// The SDK decodes the body into an error type without the status,
			// so the transport keeps its own copy and hands the SDK another.
			a.errBody, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(a.errBody))
		}
	}
	return resp, err
}
