package anthropic_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// A provider given an SDK client sends through that client's pipeline and
// credential; the request's own transport and retry count still apply.
func TestAProviderGivenAnSDKClientSendsThroughIt(t *testing.T) {
	var seen *http.Request
	client := sdk.NewClient(option.WithoutEnvironmentDefaults(), option.WithAPIKey("sk-ant-client"),
		option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			seen = r
			return next(r)
		}))
	x := run(t, testModel(), core.Request{Messages: core.Messages{user("hi")}},
		anthropic.Options{Client: &client, Env: map[string]string{anthropic.APIKeyVar: "sk-ant-ignored"}}, 200, streamFixture())
	if seen == nil || x.req == nil || x.req.Header.Get("X-Api-Key") != "sk-ant-client" {
		t.Fatalf("middleware saw %v, transport saw %v", seen != nil, x.req)
	}
	if x.err != nil || x.msg.StopReason != core.StopReasonToolUse {
		t.Fatalf("the SDK stream was not decoded: %+v, %v", x.msg, x.err)
	}
}

// Options.Headers merge into every request, and a present-nil value removes
// a header the provider would otherwise send.
func TestHeadersMergeAndANilValueRemovesADefault(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{
		Headers: map[string]*string{"x-api-key": nil, "x-gateway-key": new("gw-secret")},
	}, 200, streamFixture())
	if x.req.Header.Get("x-api-key") != "" || x.req.Header.Get("x-gateway-key") != "gw-secret" {
		t.Fatalf("headers = %v", x.req.Header)
	}
}

// Betas ride as anthropic-beta, verbatim, beside the OAuth beta an OAuth
// token needs; a request with none sends no such header.
func TestBetasAreSentAsTheBetaHeader(t *testing.T) {
	const beta = "some-feature-2026-01-01"
	for _, c := range []struct {
		betas []string
		env   map[string]string
		want  string
	}{
		{[]string{beta}, map[string]string{anthropic.APIKeyVar: "k"}, beta},
		{[]string{beta}, map[string]string{anthropic.OAuthTokenVar: "sk-ant-oat01-x"}, beta + "," + anthropic.BetaOAuth},
		{nil, map[string]string{anthropic.APIKeyVar: "k"}, ""},
	} {
		x := run(t, testModel(), core.Request{}, anthropic.Options{Betas: c.betas, Env: c.env}, 200, streamFixture())
		if got := x.req.Header.Get("anthropic-beta"); got != c.want {
			t.Errorf("betas %v with %v: anthropic-beta = %q, want %q", c.betas, c.env, got, c.want)
		}
	}
}

// The deployment resolves from Options first, then Options.Env, then
// Options.Getenv, and the process environment only when Getenv is nil; an
// empty value falls through rather than masking.
func TestTheProvidersEnvironmentIsLayered(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{anthropic.APIKeyVar: "from-getenv", anthropic.BaseURLVar: "https://getenv.example.com"}[k]
	}
	t.Setenv(anthropic.APIKeyVar, "from-process")
	for _, v := range []string{anthropic.BaseURLVar, anthropic.VertexEnableVar, anthropic.BedrockEnableVar} {
		t.Setenv(v, "")
	}
	for _, c := range []struct {
		name   string
		opts   anthropic.Options
		key    string
		prefix string
	}{
		{"Env beats Getenv", anthropic.Options{Env: map[string]string{anthropic.APIKeyVar: "from-env"}, Getenv: getenv},
			"from-env", "https://getenv.example.com/"},
		{"an empty Env value falls through", anthropic.Options{Env: map[string]string{anthropic.APIKeyVar: ""}, Getenv: getenv},
			"from-getenv", "https://getenv.example.com/"},
		{"BaseURL beats the environment", anthropic.Options{Env: map[string]string{}, Getenv: getenv, BaseURL: "https://opts.example.com"},
			"from-getenv", "https://opts.example.com/"},
		{"no Getenv reads the process", anthropic.Options{Env: map[string]string{}, Getenv: nil},
			"from-process", "https://api.anthropic.com/"},
	} {
		var r *http.Request
		c.opts.Transport = rtFunc(func(req *http.Request) (*http.Response, error) {
			r = req
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(streamFixture()))}, nil
		})
		c.opts.MaxRetries = new(0)
		core.EventStreamOf(anthropic.Provider(*testModel(), c.opts).Stream(t.Context(), core.Request{})).Wait()
		if r == nil || r.Header.Get("X-Api-Key") != c.key || !strings.HasPrefix(r.URL.String(), c.prefix) {
			t.Errorf("%s: sent %v", c.name, r)
		}
	}
}

// Options.HTTPClient carries the requests of a resolved client, here to a
// local server.
func TestTheConfiguredHTTPClientCarriesTheRequest(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streamFixture())
	}))
	defer srv.Close()
	p := anthropic.Provider(*testModel(), anthropic.Options{BaseURL: srv.URL, HTTPClient: srv.Client(),
		Env: map[string]string{anthropic.APIKeyVar: testKey}, Getenv: func(string) string { return "" }})
	msg := core.EventStreamOf(p.Stream(t.Context(), core.Request{})).Result()
	if path != "/v1/messages" || msg.StopReason != core.StopReasonToolUse {
		t.Fatalf("path %q, result %+v", path, msg)
	}
}
