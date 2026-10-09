package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// The variables Resolve reads.
const (
	APIKeyVar        = "ANTHROPIC_API_KEY"
	AuthTokenVar     = "ANTHROPIC_AUTH_TOKEN"
	OAuthTokenVar    = "ANTHROPIC_OAUTH_TOKEN"
	BaseURLVar       = "ANTHROPIC_BASE_URL"
	VertexEnableVar  = "CLAUDE_CODE_USE_VERTEX"
	VertexProjectVar = "ANTHROPIC_VERTEX_PROJECT_ID"
	GoogleProjectVar = "GOOGLE_CLOUD_PROJECT"
	VertexRegionVar  = "CLOUD_ML_REGION"
	VertexBaseURLVar = "ANTHROPIC_VERTEX_BASE_URL"
	BedrockEnableVar = "CLAUDE_CODE_USE_BEDROCK"
)

// DefaultVertexRegion is the Vertex location when CLOUD_ML_REGION is unset.
const DefaultVertexRegion = "global"

// DefaultBedrockRegion is the AWS region when neither AWS_REGION nor
// AWS_DEFAULT_REGION is set.
const DefaultBedrockRegion = "us-east-1"

// BetaOAuth is the beta the Messages API requires alongside an OAuth bearer
// (ANTHROPIC_OAUTH_TOKEN, sk-ant-oat…).
const BetaOAuth = "oauth-2025-04-20"

// cloudPlatformScope is the OAuth scope a Vertex AI call needs.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// ErrNoCredentials is returned when no deployment is selected and the
// environment holds no Anthropic credential.
var ErrNoCredentials = errors.New("anthropic: missing credentials: set " + APIKeyVar + " or " +
	AuthTokenVar + ", or select Vertex AI (" + VertexEnableVar + "=1) or Bedrock (" + BedrockEnableVar + "=1)")

// Env looks up environment variables. An empty value is treated as unset.
type Env interface {
	Lookup(name string) (string, bool)
}

// MapEnv is an Env backed by a map, for tests and for an embedder that
// configures a client without touching the process environment.
type MapEnv map[string]string

// Lookup implements Env.
func (m MapEnv) Lookup(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

// OSEnv is the process environment.
type OSEnv struct{}

// Lookup implements Env.
func (OSEnv) Lookup(name string) (string, bool) { return os.LookupEnv(name) }

// Source is the deployment a client was resolved for.
type Source string

const (
	SourceDirect  Source = "direct"
	SourceVertex  Source = "vertex"
	SourceBedrock Source = "bedrock"
)

// Resolve builds an SDK client from env, choosing the deployment:
//
//   - Vertex AI when CLAUDE_CODE_USE_VERTEX is 1 or true: project from
//     ANTHROPIC_VERTEX_PROJECT_ID or GOOGLE_CLOUD_PROJECT, location from
//     CLOUD_ML_REGION (then GOOGLE_CLOUD_LOCATION, CLOUDSDK_COMPUTE_REGION;
//     default "global"), and ANTHROPIC_VERTEX_BASE_URL as a proxy;
//   - Bedrock when CLAUDE_CODE_USE_BEDROCK is 1 or true;
//   - otherwise the Anthropic API, with ANTHROPIC_API_KEY or
//     ANTHROPIC_AUTH_TOKEN and an optional ANTHROPIC_BASE_URL.
//
// 0 or false turns a cloud deployment off whatever else is set. Resolve reads
// nothing but env and makes no network request; credentials a deployment
// fetches (Google's, AWS's) are fetched on the first request.
func Resolve(env Env) (*sdk.Client, Source, error) {
	c, d, err := resolve(env, resolveOptions{})
	if err != nil {
		return nil, "", err
	}
	return c, d.source, nil
}

// resolveOptions are what the provider adds to a resolved client.
type resolveOptions struct {
	httpClient  *http.Client
	tokenSource oauth2.TokenSource
	maxRetries  *int
	betas       []string
	baseURL     string
}

// deployment is what Resolve chose, kept for error messages.
type deployment struct {
	source     Source
	project    string
	projectVar string // the variable the project came from
	region     string
}

func resolve(env Env, ro resolveOptions) (*sdk.Client, deployment, error) {
	opts := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	if ro.httpClient != nil {
		// Before any deployment option, which wraps it rather than replacing it.
		opts = append(opts, option.WithHTTPClient(ro.httpClient))
	}
	if ro.maxRetries != nil {
		opts = append(opts, option.WithMaxRetries(*ro.maxRetries))
	}
	betas := ro.betas

	var d deployment
	switch {
	case flagOn(env, VertexEnableVar):
		d = deployment{source: SourceVertex,
			project:    firstOf(env, VertexProjectVar, GoogleProjectVar),
			projectVar: firstSet(env, VertexProjectVar, GoogleProjectVar),
			region:     firstOf(env, VertexRegionVar, "GOOGLE_CLOUD_LOCATION", "CLOUDSDK_COMPUTE_REGION")}
		if d.project == "" {
			return nil, d, fmt.Errorf("anthropic: Vertex AI is selected (%s) but no project is set: set %s or %s",
				VertexEnableVar, VertexProjectVar, GoogleProjectVar)
		}
		if d.region == "" {
			d.region = DefaultVertexRegion
		}
		opts = append(opts, withBetas(betas), vertex.WithCredentials(context.Background(), d.region, d.project,
			vertexCredentials(env, ro.tokenSource)))
		if u := get(env, VertexBaseURLVar); u != "" {
			opts = append(opts, option.WithBaseURL(withSlash(u)))
		}

	case flagOn(env, BedrockEnableVar):
		d = deployment{source: SourceBedrock, region: firstOf(env, "AWS_REGION", "AWS_DEFAULT_REGION")}
		if d.region == "" {
			d.region = DefaultBedrockRegion
		}
		cfg, err := bedrockConfig(env, d.region)
		if err != nil {
			return nil, d, err
		}
		opts = append(opts, withBetas(betas), bedrock.WithConfig(cfg))

	default:
		d = deployment{source: SourceDirect}
		switch {
		case get(env, APIKeyVar) != "":
			opts = append(opts, option.WithAPIKey(get(env, APIKeyVar)))
		case get(env, AuthTokenVar) != "":
			opts = append(opts, option.WithAuthToken(get(env, AuthTokenVar)))
		case get(env, OAuthTokenVar) != "":
			opts = append(opts, option.WithAuthToken(get(env, OAuthTokenVar)))
			betas = append(betas[:len(betas):len(betas)], BetaOAuth)
		default:
			return nil, d, ErrNoCredentials
		}
		opts = append(opts, withBetas(betas))
		if u := get(env, BaseURLVar); u != "" {
			opts = append(opts, option.WithBaseURL(withSlash(u)))
		}
	}
	if ro.baseURL != "" {
		opts = append(opts, option.WithBaseURL(withSlash(ro.baseURL)))
	}
	client := sdk.NewClient(opts...)
	return &client, d, nil
}

// bedrockConfig is the AWS configuration for a Bedrock client: static keys or
// a Bedrock bearer token from env, or else the AWS SDK's own credential chain
// (its environment variables, shared files and instance roles), which is the
// AWS SDK's domain rather than this package's.
func bedrockConfig(env Env, region string) (aws.Config, error) {
	if id, secret := get(env, "AWS_ACCESS_KEY_ID"), get(env, "AWS_SECRET_ACCESS_KEY"); id != "" && secret != "" {
		return aws.Config{Region: region,
			Credentials: credentials.NewStaticCredentialsProvider(id, secret, get(env, "AWS_SESSION_TOKEN"))}, nil
	}
	if tok := get(env, "AWS_BEARER_TOKEN_BEDROCK"); tok != "" {
		return aws.Config{Region: region, AuthSchemePreference: []string{"httpBearerAuth"},
			BearerAuthTokenProvider: bedrock.NewStaticBearerTokenProvider(tok)}, nil
	}
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region))
	if err != nil {
		return aws.Config{}, fmt.Errorf("anthropic: loading AWS configuration for Bedrock: %w", err)
	}
	return cfg, nil
}

// vertexCredentials are the Google credentials a Vertex client authorizes
// with: a Google access token in ANTHROPIC_AUTH_TOKEN (not an Anthropic
// sk-ant- token, which Google would reject), then the configured token
// source, then Application Default Credentials found on the first request.
func vertexCredentials(env Env, ts oauth2.TokenSource) *google.Credentials {
	if tok := get(env, AuthTokenVar); tok != "" && !strings.HasPrefix(tok, "sk-ant-") {
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

// flagOn reports whether a deployment flag is set to 1 or true. Any other
// value, 0 and false included, leaves the deployment off.
func flagOn(env Env, name string) bool {
	switch strings.ToLower(strings.TrimSpace(get(env, name))) {
	case "1", "true":
		return true
	}
	return false
}

func get(env Env, name string) string {
	v, _ := env.Lookup(name)
	return strings.TrimSpace(v)
}

func firstOf(env Env, names ...string) string {
	for _, n := range names {
		if v := get(env, n); v != "" {
			return v
		}
	}
	return ""
}

func firstSet(env Env, names ...string) string {
	for _, n := range names {
		if get(env, n) != "" {
			return n
		}
	}
	return ""
}

func withBetas(betas []string) option.RequestOption {
	if len(betas) == 0 {
		return option.WithHeaderDel("anthropic-beta")
	}
	return option.WithHeader("anthropic-beta", strings.Join(betas, ","))
}

func withSlash(u string) string { return strings.TrimRight(u, "/") + "/" }
