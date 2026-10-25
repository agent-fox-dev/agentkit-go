package openrouter_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

func TestNewRefusesAnIncompleteConfiguration(t *testing.T) {
	key := func(base string) map[string]string {
		return map[string]string{openrouter.APIKeyVar: "sk-or-k", openrouter.BaseURLVar: base}
	}
	for _, tc := range []struct {
		name    string
		model   string
		env     map[string]string
		is      error
		mention []string
	}{
		{"an empty model id", "", key(""), nil, []string{"model id"}},
		{"no key anywhere", "m", map[string]string{}, openrouter.ErrNoAPIKey, []string{"OPENROUTER_API_KEY"}},
		{"an empty key", "m", map[string]string{openrouter.APIKeyVar: ""}, openrouter.ErrNoAPIKey, []string{"OPENROUTER_API_KEY"}},
		{"a base that is not a URL", "m", key("not a url"), nil, []string{"OPENROUTER_BASE_URL", "not a url"}},
		{"an ftp base", "m", key("ftp://host/x"), nil, []string{"OPENROUTER_BASE_URL", "ftp://host/x"}},
		{"a relative base", "m", key("/relative"), nil, []string{"/relative"}},
		{"a base with no host", "m", key("https://"), nil, []string{`"https://"`}},
		{"a base with user-info", "m", key("ftp://user:secret@host/x"), nil, []string{`"ftp://host/x"`}},
	} {
		c, err := openrouter.New(core.Model{ID: tc.model}, openrouter.Options{Env: tc.env, Getenv: func(string) string { return "" }})
		if c != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want an error and no client", tc.name, c, err)
			continue
		}
		if tc.is != nil && !errors.Is(err, tc.is) {
			t.Errorf("%s: error %v does not wrap %v", tc.name, err, tc.is)
		}
		for _, m := range tc.mention {
			if !strings.Contains(err.Error(), m) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err, m)
			}
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: error %q shows the password", tc.name, err)
		}
	}
}

// A variable is read from Env, then Getenv, then the process environment, an
// empty value counts as unset, no other variable is asked for, and New makes
// no request.
func TestVariablesResolveFromEnvThenGetenvThenTheProcess(t *testing.T) {
	srv := newServer(t, okTurn())
	t.Setenv(openrouter.APIKeyVar, "from-process")
	t.Setenv(openrouter.BaseURLVar, srv.URL())
	var asked []string
	getenv := func(key string) string {
		asked = append(asked, key)
		switch key {
		case openrouter.APIKeyVar:
			return "from-getenv"
		case openrouter.BaseURLVar:
			return srv.URL()
		}
		return ""
	}
	for _, tc := range []struct {
		name string
		opts openrouter.Options
		want string
	}{
		{"Env first", openrouter.Options{Env: map[string]string{openrouter.APIKeyVar: "from-env"}, Getenv: getenv}, "from-env"},
		{"an empty Env value falls through", openrouter.Options{Env: map[string]string{openrouter.APIKeyVar: ""}, Getenv: getenv}, "from-getenv"},
		{"the process environment", openrouter.Options{}, "from-process"},
	} {
		before := srv.Count()
		c, err := openrouter.New(testModel(), tc.opts)
		if err != nil || srv.Count() != before {
			t.Fatalf("%s: New = %v after %d requests, want a client and none", tc.name, err, srv.Count()-before)
		}
		if o := run(t, c, userRequest("hi")); o.err != nil {
			t.Fatalf("%s: %v", tc.name, o.err)
		}
		if got := srv.Requests()[before].Header.Get("Authorization"); got != "Bearer "+tc.want {
			t.Errorf("%s: Authorization = %q, want Bearer %s", tc.name, got, tc.want)
		}
	}
	if len(asked) == 0 {
		t.Fatal("Getenv was never asked")
	}
	for _, name := range asked {
		if name != openrouter.APIKeyVar && name != openrouter.BaseURLVar {
			t.Errorf("the provider asked Getenv for %q", name)
		}
	}
}

// Every request is a POST to <base>/chat/completions asking for an event
// stream, with the key in the Authorization header only, no attribution
// headers, and nothing of another provider's environment; the default base is
// openrouter.ai/api/v1 and a configured base loses its trailing slashes.
func TestRequestsPostToChatCompletionsWithTheKeyOnlyInAuthorization(t *testing.T) {
	const key = "sk-or-secret-key-9f2c"
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-other-key")
	t.Setenv("OLLAMA_HOST", "http://ollama.internal:11434")
	t.Setenv("OPENROUTER_HTTP_REFERER", "https://example.invalid/app")
	t.Setenv("OPENROUTER_X_TITLE", "An App")

	var seen []recorded
	viaTransport := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, recorded{r.Method, r.URL.Host + r.URL.Path, r.Header.Clone(), body})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(okTurn().body))}, nil
	})}
	srv := newServer(t, okTurn())
	for _, tc := range []struct {
		name, base, wantPath string
		client               *http.Client
		got                  func() recorded
	}{
		{"the default base", "", "openrouter.ai/api/v1/chat/completions", viaTransport, func() recorded { return seen[0] }},
		{"a configured base", srv.URL() + "//", "/chat/completions", nil, func() recorded { return srv.Requests()[0] }},
	} {
		o := options(tc.base)
		o.Env[openrouter.APIKeyVar] = key
		o.HTTPClient = tc.client
		c, err := openrouter.New(testModel(), o)
		if err != nil {
			t.Fatal(err)
		}
		if out := run(t, c, userRequest("hi")); out.err != nil {
			t.Fatalf("%s: %v", tc.name, out.err)
		}
		got := tc.got()
		if got.Method != "POST" || got.Path != tc.wantPath {
			t.Errorf("%s: request = %s %s, want POST %s", tc.name, got.Method, got.Path, tc.wantPath)
		}
		if a := got.Header.Get("Authorization"); a != "Bearer "+key {
			t.Errorf("%s: Authorization = %q", tc.name, a)
		}
		if a := got.Header.Get("Accept"); a != "text/event-stream" {
			t.Errorf("%s: Accept = %q, want text/event-stream", tc.name, a)
		}
		rest := got.Header.Clone()
		rest.Del("Authorization")
		dump := fmt.Sprint(rest) + string(got.Body)
		for _, bad := range []string{key, "sk-ant-other-key", "ollama.internal", "example.invalid", "An App"} {
			if strings.Contains(dump, bad) {
				t.Errorf("%s: the request carries %q outside Authorization", tc.name, bad)
			}
		}
	}
}
