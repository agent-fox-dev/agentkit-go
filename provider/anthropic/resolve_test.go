package anthropic_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// inspect sends one request through c and returns it as it would leave the
// process, after every deployment rewrite and signature. The answer is canned
// in the client's middleware, so nothing reaches the network.
func inspect(t *testing.T, c *sdk.Client) *http.Request {
	t.Helper()
	var seen *http.Request
	_, err := c.Messages.New(context.Background(), sdk.MessageNewParams{
		Model: "claude-test", MaxTokens: 1, Messages: []sdk.MessageParam{sdk.NewUserMessage(sdk.NewTextBlock("hi"))},
	}, option.WithMaxRetries(0), option.WithMiddleware(func(r *http.Request, _ option.MiddlewareNext) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 200, Request: r, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"m","type":"message","role":"assistant","model":"claude-test",` +
				`"content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
	}))
	if err != nil {
		t.Fatalf("request through the resolved client: %v", err)
	}
	return seen
}

// Resolve picks the deployment from the Env alone and builds a client whose
// first request carries that deployment's URL and credential.
func TestResolveBuildsTheDeploymentTheEnvSelects(t *testing.T) {
	direct := "https://api.anthropic.com/v1/messages"
	header := func(name, want string) func(*http.Request) bool {
		return func(r *http.Request) bool { return r.Header.Get(name) == want }
	}
	cases := []struct {
		name string
		env  anthropic.MapEnv
		src  anthropic.Source
		url  string
		auth func(*http.Request) bool
	}{
		{"an API key", anthropic.MapEnv{"ANTHROPIC_API_KEY": "sk-ant-k"}, anthropic.SourceDirect, direct, header("X-Api-Key", "sk-ant-k")},
		{"a base URL", anthropic.MapEnv{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": "https://gw.internal/"},
			anthropic.SourceDirect, "https://gw.internal/v1/messages", header("X-Api-Key", "k")},
		{"an auth token is a bearer", anthropic.MapEnv{"ANTHROPIC_AUTH_TOKEN": "tok"}, anthropic.SourceDirect, direct,
			header("Authorization", "Bearer tok")},
		{"an OAuth token is a bearer with the OAuth beta", anthropic.MapEnv{"ANTHROPIC_OAUTH_TOKEN": "sk-ant-oat01-x"},
			anthropic.SourceDirect, direct, func(r *http.Request) bool {
				return r.Header.Get("Authorization") == "Bearer sk-ant-oat01-x" && r.Header.Get("anthropic-beta") == anthropic.BetaOAuth
			}},
		{"the API key outranks the bearers", anthropic.MapEnv{"ANTHROPIC_API_KEY": "key", "ANTHROPIC_AUTH_TOKEN": "tok",
			"ANTHROPIC_OAUTH_TOKEN": "oa"}, anthropic.SourceDirect, direct, func(r *http.Request) bool {
			return r.Header.Get("X-Api-Key") == "key" && r.Header.Get("Authorization") == "" && r.Header.Get("anthropic-beta") == ""
		}},
		{"the auth token outranks the OAuth token", anthropic.MapEnv{"ANTHROPIC_AUTH_TOKEN": "tok", "ANTHROPIC_OAUTH_TOKEN": "oa"},
			anthropic.SourceDirect, direct, header("Authorization", "Bearer tok")},
		{"off flags fall through to direct", anthropic.MapEnv{"CLAUDE_CODE_USE_VERTEX": "0", "GOOGLE_CLOUD_PROJECT": "p",
			"CLAUDE_CODE_USE_BEDROCK": "false", "AWS_REGION": "us-west-2", "ANTHROPIC_API_KEY": "k"},
			anthropic.SourceDirect, direct, header("X-Api-Key", "k")},
		{"Vertex with a proxy", anthropic.MapEnv{"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_VERTEX_PROJECT_ID": "my-proj",
			"CLOUD_ML_REGION": "europe-west1", "ANTHROPIC_VERTEX_BASE_URL": "https://proxy.internal",
			"ANTHROPIC_AUTH_TOKEN": "ya29.tok"}, anthropic.SourceVertex,
			"https://proxy.internal/v1/projects/my-proj/locations/europe-west1/publishers/anthropic/models/claude-test:rawPredict",
			header("X-Api-Key", "")},
		{"Vertex's default location, an API key notwithstanding", anthropic.MapEnv{"CLAUDE_CODE_USE_VERTEX": "true",
			"GOOGLE_CLOUD_PROJECT": "p1", "ANTHROPIC_AUTH_TOKEN": "ya29.tok", "ANTHROPIC_API_KEY": "sk-ant-x"}, anthropic.SourceVertex,
			"https://aiplatform.googleapis.com/v1/projects/p1/locations/global/publishers/anthropic/models/claude-test:rawPredict",
			header("X-Api-Key", "")},
		{"Bedrock with static keys", anthropic.MapEnv{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-west-2",
			"AWS_ACCESS_KEY_ID": "AKIDTEST", "AWS_SECRET_ACCESS_KEY": "secret"}, anthropic.SourceBedrock,
			"https://bedrock-runtime.us-west-2.amazonaws.com/model/claude-test/invoke", func(r *http.Request) bool {
				return strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDTEST/")
			}},
		{"Bedrock with a bearer token and the default region", anthropic.MapEnv{"CLAUDE_CODE_USE_BEDROCK": "TRUE",
			"AWS_BEARER_TOKEN_BEDROCK": "brt"}, anthropic.SourceBedrock,
			"https://bedrock-runtime.us-east-1.amazonaws.com/model/claude-test/invoke", header("Authorization", "Bearer brt")},
	}
	for _, c := range cases {
		client, src, err := anthropic.Resolve(c.env)
		if err != nil || src != c.src {
			t.Errorf("%s: source %q, err %v; want %q", c.name, src, err, c.src)
			continue
		}
		r := inspect(t, client)
		if r.URL.String() != c.url || !c.auth(r) {
			t.Errorf("%s: request %s with headers %v, want %s", c.name, r.URL, r.Header, c.url)
		}
	}
}

// Resolve reads only the Env it is given; OSEnv is the process environment.
func TestResolveReadsOnlyTheEnvItIsGiven(t *testing.T) {
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "1")
	t.Setenv("ANTHROPIC_API_KEY", "from-the-process")
	t.Setenv(anthropic.VertexProjectVar, "")
	t.Setenv(anthropic.GoogleProjectVar, "")
	_, src, err := anthropic.Resolve(anthropic.MapEnv{"ANTHROPIC_API_KEY": "sk-ant-test"})
	if err != nil || src != anthropic.SourceDirect {
		t.Fatalf("source %q, err %v; the process environment must not be read", src, err)
	}
	if v, ok := (anthropic.OSEnv{}).Lookup("ANTHROPIC_API_KEY"); !ok || v != "from-the-process" {
		t.Fatalf("OSEnv.Lookup = %q, %v", v, ok)
	}
	if _, src, err := anthropic.Resolve(anthropic.OSEnv{}); err == nil || src != "" {
		t.Fatalf("OSEnv with the Vertex flag and no project: %q, %v", src, err)
	}
}

// Bedrock with no credential in the Env leaves them to the AWS SDK's own
// chain, which is looked up on the first request, not by Resolve.
func TestBedrockWithoutKeysDefersToTheAWSChain(t *testing.T) {
	testutil.Hermetic(t)
	for _, v := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE", "AWS_REGION"} {
		t.Setenv(v, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	if c, src, err := anthropic.Resolve(anthropic.MapEnv{"CLAUDE_CODE_USE_BEDROCK": "1"}); err != nil || src != anthropic.SourceBedrock || c == nil {
		t.Fatalf("source %q, err %v", src, err)
	}
}

// With no credential and no deployment the error names every way to set one;
// a selected Vertex with no project names the variables that fix it.
func TestResolveFailsSayingWhatIsMissing(t *testing.T) {
	c, _, err := anthropic.Resolve(anthropic.MapEnv{})
	if c != nil || !errors.Is(err, anthropic.ErrNoCredentials) {
		t.Fatalf("client %v, err %v; want ErrNoCredentials", c, err)
	}
	for _, v := range []string{anthropic.APIKeyVar, anthropic.AuthTokenVar, anthropic.OAuthTokenVar,
		anthropic.VertexEnableVar, anthropic.BedrockEnableVar} {
		if !strings.Contains(err.Error(), v) {
			t.Errorf("the error does not name %s: %v", v, err)
		}
	}
	_, _, err = anthropic.Resolve(anthropic.MapEnv{"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_API_KEY": "k"})
	if err == nil || !strings.Contains(err.Error(), anthropic.VertexProjectVar) || !strings.Contains(err.Error(), anthropic.GoogleProjectVar) {
		t.Fatalf("Vertex with no project: err = %v", err)
	}
}
