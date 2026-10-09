package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// inspect sends one request through c and returns it as it would have left
// the process, after every deployment rewrite and signature. The response is
// canned inside the client's middleware, so nothing reaches the network — not
// even a credential lookup, which happens beneath it in the HTTP transport.
func inspect(t *testing.T, c *sdk.Client) *http.Request {
	t.Helper()
	var seen *http.Request
	_, err := c.Messages.New(context.Background(), sdk.MessageNewParams{
		Model:     "claude-test",
		MaxTokens: 1,
		Messages:  []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hi"))},
	}, option.WithMaxRetries(0), option.WithMiddleware(func(r *http.Request, _ option.MiddlewareNext) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 200, Request: r,
			Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"m","type":"message","role":"assistant",` +
				`"model":"claude-test","content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
	}))
	if err != nil {
		t.Fatalf("request through the resolved client: %v", err)
	}
	if seen == nil {
		t.Fatal("the request never reached the client's middleware")
	}
	return seen
}

// TS-10-4: Resolve reads only the Env it is given, and the three sources are
// distinct constants.
func TestResolveReadsAnInjectedEnv_TS10_4(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	c, src, err := anthropic.Resolve(anthropic.MapEnv{"ANTHROPIC_API_KEY": "sk-ant-test"})
	if err != nil {
		t.Fatal(err)
	}
	if src != anthropic.SourceDirect || c == nil {
		t.Fatalf("source = %q, client nil = %v; the process environment must not be read", src, c == nil)
	}
	if v, ok := (anthropic.MapEnv{"A": "b"}).Lookup("A"); !ok || v != "b" {
		t.Fatalf("MapEnv.Lookup = %q, %v", v, ok)
	}
	seen := map[anthropic.Source]bool{}
	for _, s := range []anthropic.Source{anthropic.SourceDirect, anthropic.SourceVertex, anthropic.SourceBedrock} {
		if s == "" || seen[s] {
			t.Fatalf("source %q is empty or repeated", s)
		}
		seen[s] = true
	}
}

// TS-10-5: the Vertex flag builds a Vertex client from the project, region
// and proxy base URL.
func TestResolveVertex_TS10_5(t *testing.T) {
	c, src, err := anthropic.Resolve(anthropic.MapEnv{
		"CLAUDE_CODE_USE_VERTEX":      "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "my-proj",
		"CLOUD_ML_REGION":             "europe-west1",
		"ANTHROPIC_VERTEX_BASE_URL":   "https://custom-proxy.internal",
		"ANTHROPIC_AUTH_TOKEN":        "ya29.test-google-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if src != anthropic.SourceVertex || c == nil {
		t.Fatalf("source = %q, client nil = %v", src, c == nil)
	}
	r := inspect(t, c)
	if r.URL.Host != "custom-proxy.internal" {
		t.Fatalf("host = %s, want the Vertex proxy", r.URL.Host)
	}
	if want := "/v1/projects/my-proj/locations/europe-west1/publishers/anthropic/models/claude-test:rawPredict"; r.URL.Path != want {
		t.Fatalf("path = %s, want %s", r.URL.Path, want)
	}
	if r.Header.Get("X-Api-Key") != "" {
		t.Fatal("a Vertex request carries an Anthropic API key header")
	}
}

// TS-10-6: the Bedrock flag builds a Bedrock client.
func TestResolveBedrock_TS10_6(t *testing.T) {
	c, src, err := anthropic.Resolve(anthropic.MapEnv{"CLAUDE_CODE_USE_BEDROCK": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if src != anthropic.SourceBedrock || c == nil {
		t.Fatalf("source = %q, client nil = %v", src, c == nil)
	}
}

// TS-10-7: with no cloud flag, a key and a base URL build a direct client.
func TestResolveDirect_TS10_7(t *testing.T) {
	c, src, err := anthropic.Resolve(anthropic.MapEnv{
		"ANTHROPIC_API_KEY":  "sk-ant-api03",
		"ANTHROPIC_BASE_URL": "https://api.anthropic.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if src != anthropic.SourceDirect || c == nil {
		t.Fatalf("source = %q, client nil = %v", src, c == nil)
	}
	r := inspect(t, c)
	if r.URL.Host != "api.anthropic.internal" || r.URL.Path != "/v1/messages" {
		t.Fatalf("request = %s", r.URL)
	}
	if r.Header.Get("X-Api-Key") != "sk-ant-api03" {
		t.Fatalf("x-api-key = %q", r.Header.Get("X-Api-Key"))
	}

	// An auth token is the other direct credential, sent as a bearer.
	c, src, err = anthropic.Resolve(anthropic.MapEnv{"ANTHROPIC_AUTH_TOKEN": "tok-123"})
	if err != nil || src != anthropic.SourceDirect {
		t.Fatalf("auth token: %q, %v", src, err)
	}
	if got := inspect(t, c).Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", got)
	}
}

// TS-10-8: no cloud flag and no credential is an error naming credentials,
// with no client.
func TestResolveWithoutCredentialsFails_TS10_8(t *testing.T) {
	c, _, err := anthropic.Resolve(anthropic.MapEnv{})
	if c != nil {
		t.Fatal("a client was returned with no credential")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "credentials") {
		t.Fatalf("err = %v, want one saying credentials are missing", err)
	}
}

// TS-10-9: an explicit off flag disables the deployment even when its
// coordinates are present, and resolution falls through to direct.
func TestAnOffFlagFallsThroughToDirect_TS10_9(t *testing.T) {
	for _, off := range []string{"0", "false"} {
		_, src, err := anthropic.Resolve(anthropic.MapEnv{
			"CLAUDE_CODE_USE_VERTEX": off, "GOOGLE_CLOUD_PROJECT": "my-project",
			"CLAUDE_CODE_USE_BEDROCK": off, "AWS_REGION": "us-west-2",
			"ANTHROPIC_API_KEY": "sk-ant-test",
		})
		if err != nil || src != anthropic.SourceDirect {
			t.Fatalf("off=%s: source = %q, err = %v; want direct", off, src, err)
		}
	}
}

// TS-10-10: every deployment's client is checked offline, by the URL and
// credential its first request would carry.
func TestResolvedClientsAreVerifiedOffline_TS10_10(t *testing.T) {
	cases := []struct {
		name     string
		env      anthropic.MapEnv
		src      anthropic.Source
		host     string
		pathPart string
		auth     func(*http.Request) bool
	}{
		{"direct", anthropic.MapEnv{"ANTHROPIC_API_KEY": "sk-ant-k"}, anthropic.SourceDirect,
			"api.anthropic.com", "/v1/messages",
			func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == "sk-ant-k" }},
		{"vertex", anthropic.MapEnv{"CLAUDE_CODE_USE_VERTEX": "1", "GOOGLE_CLOUD_PROJECT": "p1",
			"ANTHROPIC_AUTH_TOKEN": "ya29.tok"}, anthropic.SourceVertex,
			"aiplatform.googleapis.com", "/projects/p1/locations/global/",
			func(r *http.Request) bool { return r.Header.Get("X-Api-Key") == "" }},
		{"bedrock", anthropic.MapEnv{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-west-2",
			"AWS_ACCESS_KEY_ID": "AKIDTEST", "AWS_SECRET_ACCESS_KEY": "secret"}, anthropic.SourceBedrock,
			"bedrock-runtime.us-west-2.amazonaws.com", "/model/claude-test/invoke",
			func(r *http.Request) bool {
				return strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDTEST/")
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, src, err := anthropic.Resolve(tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if src != tc.src {
				t.Fatalf("source = %q, want %q", src, tc.src)
			}
			r := inspect(t, c)
			if r.URL.Host != tc.host || !strings.Contains(r.URL.Path, tc.pathPart) {
				t.Fatalf("request = %s, want host %s and path containing %s", r.URL, tc.host, tc.pathPart)
			}
			if !tc.auth(r) {
				t.Fatalf("credential headers: %v", r.Header)
			}
		})
	}
}
