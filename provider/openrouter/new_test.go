package openrouter_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// ------------------------------------------------------------------ harness

// neverDial is an HTTP client whose transport fails the test if it is used:
// a provider that makes a request at construction time is caught by it.
func neverDial(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request was made to %s", r.URL)
		return nil, errors.New("no network in this test")
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testModel() core.Model {
	return core.Model{ID: "vendor/model", ContextWindow: 100000, MaxOutputTokens: 4096}
}

// ------------------------------------------------------------------- tests

// TS-27-1: New with a key and a model id returns a client without making a
// request, and the exported constants hold their values.
func TestNewBuildsAClientWithoutARequest_TS27_1(t *testing.T) {
	srv := newReplay(t)
	env := map[string]string{openrouter.APIKeyVar: "sk-or-test", openrouter.BaseURLVar: srv.URL()}
	c, err := openrouter.New(testModel(), openrouter.Options{Env: env, HTTPClient: neverDial(t)})
	if err != nil || c == nil {
		t.Fatalf("New = %v, %v; want a client", c, err)
	}
	if srv.Count() != 0 {
		t.Errorf("the server received %d requests at construction, want 0", srv.Count())
	}
	if openrouter.API != "openrouter-chat" || openrouter.APIKeyVar != "OPENROUTER_API_KEY" ||
		openrouter.BaseURLVar != "OPENROUTER_BASE_URL" || openrouter.DefaultBaseURL != "https://openrouter.ai/api/v1" ||
		openrouter.DefaultMaxRetries != 2 {
		t.Errorf("constants = %q %q %q %q %d", openrouter.API, openrouter.APIKeyVar, openrouter.BaseURLVar,
			openrouter.DefaultBaseURL, openrouter.DefaultMaxRetries)
	}
	var _ core.ProviderClient = c
}

// TS-27-3: a missing or empty API key fails New with ErrNoAPIKey, which names
// the variable, and no request is made.
func TestNewWithoutAKeyFails_TS27_3(t *testing.T) {
	srv := newReplay(t)
	none := func(string) string { return "" }
	for name, opts := range map[string]openrouter.Options{
		"no key anywhere": {Env: map[string]string{openrouter.BaseURLVar: srv.URL()}, Getenv: none},
		"empty key":       {Env: map[string]string{openrouter.APIKeyVar: "", openrouter.BaseURLVar: srv.URL()}, Getenv: none},
	} {
		opts.HTTPClient = neverDial(t)
		c, err := openrouter.New(testModel(), opts)
		if c != nil {
			t.Errorf("%s: New returned a client", name)
		}
		if !errors.Is(err, openrouter.ErrNoAPIKey) {
			t.Errorf("%s: error = %v, want one wrapping ErrNoAPIKey", name, err)
			continue
		}
		if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
			t.Errorf("%s: error %q does not name OPENROUTER_API_KEY", name, err)
		}
	}
	if srv.Count() != 0 {
		t.Errorf("the server received %d requests, want 0", srv.Count())
	}
}

// TS-27-4: a base URL that is not an absolute http or https URL fails New,
// naming the variable and showing the value without user-info.
func TestNewWithAMalformedBaseURLFails_TS27_4(t *testing.T) {
	for _, tc := range []struct{ in, shown string }{
		{"not a url", "not a url"},
		{"ftp://host/x", "ftp://host/x"},
		{"/relative", "/relative"},
		{"ftp://user:secret@host/x", "ftp://host/x"},
		{"https://", "https://"},
	} {
		env := map[string]string{openrouter.APIKeyVar: "sk-or-test", openrouter.BaseURLVar: tc.in}
		c, err := openrouter.New(testModel(), openrouter.Options{Env: env, HTTPClient: neverDial(t)})
		if c != nil || err == nil {
			t.Errorf("%q: New = %v, %v; want an error and no client", tc.in, c, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "OPENROUTER_BASE_URL") || !strings.Contains(msg, tc.shown) {
			t.Errorf("%q: error %q does not name OPENROUTER_BASE_URL and show %q", tc.in, msg, tc.shown)
		}
		if strings.Contains(msg, "secret") {
			t.Errorf("%q: error %q shows the password", tc.in, msg)
		}
	}
}

// TS-27-5: an empty model id fails New.
func TestNewWithAnEmptyModelIDFails_TS27_5(t *testing.T) {
	env := map[string]string{openrouter.APIKeyVar: "sk-or-test"}
	c, err := openrouter.New(core.Model{}, openrouter.Options{Env: env, HTTPClient: neverDial(t)})
	if c != nil || err == nil {
		t.Errorf("New = %v, %v; want an error and no client", c, err)
	}
}
