package anthropic

import (
	"net/http"
	"os"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

// providerEnv is the Env a provider resolves through: the explicit Options
// settings, then a request's own overrides, then Options.Getenv (os.Getenv
// when nil).
type providerEnv struct {
	set      map[string]string
	override map[string]string
	getenv   func(string) string
}

func (e providerEnv) Lookup(name string) (string, bool) {
	if v := e.set[name]; v != "" {
		return v, true
	}
	if v := e.override[name]; v != "" {
		return v, true
	}
	if e.getenv != nil {
		v := e.getenv(name)
		return v, v != ""
	}
	return os.LookupEnv(name)
}

// sdkClient is the official SDK client a request goes through: the one the
// caller configured, or one Resolve builds from the environment.
//
// transport, when set, is the request's own round tripper. It goes beneath
// any deployment wrapping, so Vertex still authorizes a request it carries.
func (c *client) sdkClient(override map[string]string, transport http.RoundTripper) (*sdk.Client, deployment, Env, error) {
	env := providerEnv{set: map[string]string{}, override: override, getenv: c.opts.Getenv}
	if c.opts.Client != nil {
		return c.opts.Client, deployment{source: SourceDirect}, env, nil
	}
	if c.opts.VertexProject != "" {
		// An explicit project selects Vertex whatever the environment says.
		env.set[VertexEnableVar] = "1"
		env.set[VertexProjectVar] = c.opts.VertexProject
	}
	if c.opts.VertexLocation != "" {
		env.set[VertexRegionVar] = c.opts.VertexLocation
	}
	ro := resolveOptions{httpClient: c.opts.HTTPClient, tokenSource: c.opts.VertexTokenSource,
		maxRetries: c.opts.MaxRetries, betas: c.opts.Betas, baseURL: c.opts.BaseURL}
	if transport != nil {
		ro.httpClient = &http.Client{Transport: transport}
	}
	sc, d, err := resolve(env, ro)
	return sc, d, env, err
}
