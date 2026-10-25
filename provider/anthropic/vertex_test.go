package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

const vertexPath = "/v1/projects/proj-1/locations/us-east5/publishers/anthropic/models/claude-test:streamRawPredict"

// sentTo is the URL a turn with opts and env goes to.
func sentTo(t *testing.T, opts anthropic.Options, env map[string]string) *http.Request {
	t.Helper()
	opts.Env = env
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	x := run(t, testModel(), core.Request{Messages: core.Messages{user("hi")}}, opts, 200, streamFixture())
	if x.req == nil {
		t.Fatalf("no request was sent: %+v", x.msg)
	}
	return x.req
}

// Moving between the direct API and Vertex is a configuration change: the
// flag selects Vertex, an explicit off vetoes every other signal, Options
// outrank the environment, and leftover Vertex-only variables select nothing.
func TestTheDeploymentIsAConfigurationChange(t *testing.T) {
	direct := "https://api.anthropic.com/v1/messages"
	regional := "https://us-east5-aiplatform.googleapis.com" + vertexPath
	cases := []struct {
		name string
		opts anthropic.Options
		env  map[string]string
		want string
	}{
		{"an API key is direct", anthropic.Options{}, map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"}, direct},
		{"a project on Options", anthropic.Options{VertexProject: "proj-1", VertexLocation: "us-east5"}, nil, regional},
		{"the flag and its coordinates", anthropic.Options{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-1", "CLOUD_ML_REGION": "us-east5"}, regional},
		{"the ambient GCP project and location", anthropic.Options{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": "true",
			"GOOGLE_CLOUD_PROJECT": "proj-1", "GOOGLE_CLOUD_LOCATION": "us-east5"}, regional},
		{"the Vertex proxy beats the general base URL", anthropic.Options{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-1", "CLOUDSDK_COMPUTE_REGION": "us-east5",
			"ANTHROPIC_BASE_URL": "https://general.example.com", "ANTHROPIC_VERTEX_BASE_URL": "https://vertex.example.com"},
			"https://vertex.example.com" + vertexPath},
		{"no location is global", anthropic.Options{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-1"},
			"https://aiplatform.googleapis.com/v1/projects/proj-1/locations/global/publishers/anthropic/models/claude-test:streamRawPredict"},
		{"a leftover project does not select Vertex", anthropic.Options{}, map[string]string{"ANTHROPIC_API_KEY": "k",
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-from-last-week"}, direct},
		{"a leftover Vertex proxy does not either", anthropic.Options{}, map[string]string{"ANTHROPIC_API_KEY": "k",
			"ANTHROPIC_VERTEX_BASE_URL": "https://us-east5-aiplatform.googleapis.com"}, direct},
		{"Options outrank an off flag", anthropic.Options{VertexProject: "proj-1", VertexLocation: "us-east5"},
			map[string]string{"CLAUDE_CODE_USE_VERTEX": "0", "ANTHROPIC_API_KEY": "k"}, regional},
	}
	for _, off := range []string{"0", "false", "no", "off", "OFF"} {
		cases = append(cases, struct {
			name string
			opts anthropic.Options
			env  map[string]string
			want string
		}{"the flag set to " + off, anthropic.Options{}, map[string]string{"CLAUDE_CODE_USE_VERTEX": off,
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-1", "ANTHROPIC_API_KEY": "k"}, direct})
	}
	for _, c := range cases {
		if got := sentTo(t, c.opts, c.env).URL.String(); got != c.want {
			t.Errorf("%s: sent to\n  %s\nwant\n  %s", c.name, got, c.want)
		}
	}
}

// Vertex authorizes with a Google credential; no Anthropic-issued credential
// left in the environment ever reaches a Google endpoint.
func TestAnthropicCredentialsAreNeverSentToVertex(t *testing.T) {
	vertex := func(kv ...string) map[string]string {
		env := map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "ANTHROPIC_VERTEX_PROJECT_ID": "proj-1"}
		for i := 0; i+1 < len(kv); i += 2 {
			env[kv[i]] = kv[i+1]
		}
		return env
	}
	for _, c := range []struct {
		name   string
		opts   anthropic.Options
		env    map[string]string
		bearer string
	}{
		{"an API key", anthropic.Options{}, vertex("ANTHROPIC_API_KEY", "sk-ant-api03-SECRET"), testGoogleToken},
		{"an OAuth token", anthropic.Options{}, vertex("ANTHROPIC_OAUTH_TOKEN", "sk-ant-oat01-SECRET"), testGoogleToken},
		{"an Anthropic token as the auth token", anthropic.Options{}, vertex("ANTHROPIC_AUTH_TOKEN", "sk-ant-oat01-SECRET"), testGoogleToken},
		{"Vertex chosen on Options", anthropic.Options{VertexProject: "proj-1"},
			map[string]string{"ANTHROPIC_OAUTH_TOKEN": "sk-ant-oat01-SECRET"}, testGoogleToken},
		{"a Google token survives a leftover key", anthropic.Options{},
			vertex("ANTHROPIC_AUTH_TOKEN", "ya29.access", "ANTHROPIC_API_KEY", "sk-ant-api03-SECRET"), "ya29.access"},
	} {
		r := sentTo(t, c.opts, c.env)
		if !strings.Contains(r.URL.Host, "aiplatform.googleapis.com") || r.Header.Get("x-api-key") != "" ||
			r.Header.Get("Authorization") != "Bearer "+c.bearer {
			t.Errorf("%s: sent to %s with x-api-key %q, Authorization %q", c.name, r.URL.Host,
				r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
		}
	}
}

// A Vertex body names the model in the URL, not the body, and carries the
// Vertex version; the direct body is untouched by either.
func TestTheVertexBodyNamesNoModelAndCarriesItsVersion(t *testing.T) {
	for _, c := range []struct {
		opts           anthropic.Options
		env            map[string]string
		model, version any
	}{
		{anthropic.Options{VertexProject: "proj-1"}, nil, nil, anthropic.VertexAPIVersion},
		{anthropic.Options{}, map[string]string{"ANTHROPIC_API_KEY": "k"}, "claude-test", nil},
	} {
		c.opts.Env = c.env
		x := run(t, testModel(), core.Request{Messages: core.Messages{user("hi")}}, c.opts, 200, streamFixture())
		body := x.sent(t)
		if body["model"] != c.model || body["anthropic_version"] != c.version {
			t.Errorf("Vertex project %q: model %v, anthropic_version %v", c.opts.VertexProject, body["model"], body["anthropic_version"])
		}
	}
}

// A Vertex selection that cannot be resolved fails the turn with the reason
// and sends nothing, rather than falling back to the direct API.
func TestAnUnresolvableVertexDeploymentFailsWithoutSending(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{Env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}}, 200, streamFixture())
	if x.req != nil || !strings.Contains(x.msg.ErrorMessage, anthropic.VertexProjectVar) || x.msg.StopReason != core.StopReasonError {
		t.Fatalf("sent %v, result %+v", x.req != nil, x.msg)
	}

	// With no Google credential configured, Application Default Credentials
	// are looked for on the first request, and their absence fails it.
	testutil.Hermetic(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	reached := false
	p := anthropic.Provider(*testModel(), anthropic.Options{VertexProject: "proj-1", Getenv: func(string) string { return "" }, MaxRetries: new(0),
		Transport: rtFunc(func(*http.Request) (*http.Response, error) { reached = true; return nil, io.EOF })})
	msg := core.EventStreamOf(p.Stream(context.Background(), core.Request{})).Result()
	if reached || msg.StopReason != core.StopReasonError || !strings.Contains(msg.ErrorMessage, "missing.json") {
		t.Fatalf("sent %v, result %+v", reached, msg)
	}
}

// A Vertex 401 or 403 names the deployment, what selected it, the Anthropic
// credentials it withheld and how to go direct; other failures, and the
// direct deployment's own, carry no such note.
func TestAVertexAuthFailureExplainsTheDeployment(t *testing.T) {
	for _, c := range []struct {
		name     string
		opts     anthropic.Options
		status   int
		want     []string
		unwanted []string
	}{
		{"a 401 with an OAuth token set", anthropic.Options{Env: map[string]string{"CLAUDE_CODE_USE_VERTEX": "1",
			"ANTHROPIC_VERTEX_PROJECT_ID": "proj-1", "ANTHROPIC_OAUTH_TOKEN": "sk-ant-oat01-x"}}, 401,
			[]string{"Vertex AI", "project proj-1 (from ANTHROPIC_VERTEX_PROJECT_ID)", "location global",
				"ANTHROPIC_OAUTH_TOKEN is set, and is never sent to a Google endpoint", "CLAUDE_CODE_USE_VERTEX=0"},
			[]string{"ANTHROPIC_API_KEY is set", "ANTHROPIC_API_KEY and"}},
		{"a 403 with both set", anthropic.Options{VertexProject: "proj-1", VertexLocation: "us-east5",
			Env: map[string]string{"ANTHROPIC_API_KEY": "k", "ANTHROPIC_OAUTH_TOKEN": "o"}}, 403,
			[]string{"project proj-1 (from Options.VertexProject)", "location us-east5", "ANTHROPIC_API_KEY and ANTHROPIC_OAUTH_TOKEN are set, and are never sent"},
			[]string{"(from ANTHROPIC_VERTEX_PROJECT_ID)"}},
		{"a 500 from Vertex", anthropic.Options{VertexProject: "proj-1"}, 500, nil, []string{"Vertex"}},
		{"a direct 401", anthropic.Options{Env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-wrong"}}, 401,
			[]string{"HTTP 401"}, []string{"Vertex"}},
	} {
		msg := run(t, testModel(), core.Request{}, c.opts, c.status, `{"error":{"code":401,"message":"missing credential"}}`).msg
		for _, w := range c.want {
			if !strings.Contains(msg.ErrorMessage, w) {
				t.Errorf("%s: error does not say %q:\n%s", c.name, w, msg.ErrorMessage)
			}
		}
		for _, u := range c.unwanted {
			if strings.Contains(msg.ErrorMessage, u) {
				t.Errorf("%s: error says %q:\n%s", c.name, u, msg.ErrorMessage)
			}
		}
	}
}
