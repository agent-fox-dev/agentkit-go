package anthropic

import (
	"context"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// cloudPlatformScope is the OAuth scope a Vertex AI call needs.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// sdkClient is the official SDK client a request goes through: the one the
// caller configured, or one built from the environment.
//
// transport, when set, is the request's own round tripper. It goes beneath
// any deployment wrapping, so Vertex still authorizes a request it carries.
func (c *client) sdkClient(ctx context.Context, m *core.Model, env provider.Env, vx Vertex, transport http.RoundTripper) *sdk.Client {
	if c.opts.Client != nil {
		return c.opts.Client
	}
	opts := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	switch {
	case transport != nil:
		opts = append(opts, option.WithHTTPClient(&http.Client{Transport: transport}))
	case c.opts.HTTPClient != nil:
		// Passed before the Vertex option, so Vertex wraps it with OAuth
		// rather than replacing it.
		opts = append(opts, option.WithHTTPClient(c.opts.HTTPClient))
	}
	if c.opts.MaxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*c.opts.MaxRetries))
	}
	betas := c.opts.Betas
	if vx.On() {
		if len(betas) > 0 {
			opts = append(opts, option.WithHeader("anthropic-beta", strings.Join(betas, ",")))
		}
		opts = append(opts, vertex.WithCredentials(ctx, vx.Location, vx.Project, vertexCredentials(env, c.opts.VertexTokenSource)))
		if u := env.Get(VertexBaseURLVar); u != "" {
			opts = append(opts, option.WithBaseURL(strings.TrimRight(u, "/")+"/"))
		} else if c.opts.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(c.opts.BaseURL))
		}
		client := sdk.NewClient(opts...)
		return &client
	}

	base := c.opts.BaseURL
	if base == "" {
		base = env.Get(BaseURLVar)
	}
	if base == "" {
		base = m.BaseURL
	}
	if base != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(base, "/")+"/"))
	}
	// The API key first, as in the official SDKs; then a bearer token. An
	// OAuth token (sk-ant-oat…) also needs the OAuth beta.
	switch {
	case env.Get(APIKeyVar) != "":
		opts = append(opts, option.WithAPIKey(env.Get(APIKeyVar)))
	case env.Get(AuthTokenVar) != "":
		opts = append(opts, option.WithAuthToken(env.Get(AuthTokenVar)))
	case env.Get(OAuthTokenVar) != "":
		opts = append(opts, option.WithAuthToken(env.Get(OAuthTokenVar)))
		betas = append(betas[:len(betas):len(betas)], BetaOAuth)
	}
	if len(betas) > 0 {
		opts = append(opts, option.WithHeader("anthropic-beta", strings.Join(betas, ",")))
	}
	client := sdk.NewClient(opts...)
	return &client
}

// vertexCredentials are the Google credentials a Vertex client authorizes
// with. A Google access token in ANTHROPIC_AUTH_TOKEN (not an Anthropic
// sk-ant- token, which Google would reject) is used as it is; then the
// configured token source; otherwise Application Default Credentials are
// found on first use, so building the client needs neither a network nor a
// credential.
func vertexCredentials(env provider.Env, ts oauth2.TokenSource) *google.Credentials {
	if tok := env.Get(AuthTokenVar); tok != "" && !strings.HasPrefix(tok, "sk-ant-") {
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: tok})}
	}
	if ts != nil {
		return &google.Credentials{TokenSource: ts}
	}
	return &google.Credentials{TokenSource: &adcSource{}}
}

// adcSource defers finding Application Default Credentials to the first
// token request.
type adcSource struct {
	once sync.Once
	src  oauth2.TokenSource
	err  error
}

func (a *adcSource) Token() (*oauth2.Token, error) {
	a.once.Do(func() {
		creds, err := google.FindDefaultCredentials(context.Background(), cloudPlatformScope)
		if err != nil {
			a.err = err
			return
		}
		a.src = creds.TokenSource
	})
	if a.err != nil {
		return nil, a.err
	}
	return a.src.Token()
}
