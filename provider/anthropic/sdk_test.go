package anthropic_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// TS-10-3: a provider given an SDK client sends its request through that
// client's pipeline and decodes the SDK's stream; no hand-rolled SSE reader
// or transport remains.
func TestTheProviderDelegatesToTheSDKClient_TS10_3(t *testing.T) {
	var seen *http.Request
	client := sdk.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithAPIKey("sk-ant-test"),
		option.WithMaxRetries(0),
		option.WithMiddleware(func(r *http.Request, _ option.MiddlewareNext) (*http.Response, error) {
			seen = r
			return &http.Response{StatusCode: 200, Request: r,
				Header: http.Header{"Content-Type": {"text/event-stream"}},
				Body:   io.NopCloser(strings.NewReader(streamFixture()))}, nil
		}),
	)
	p := anthropic.Provider(*testModel(), anthropic.Options{Client: &client})
	s := stream(p, context.Background(), core.Request{
		Messages: core.Messages{core.UserMessage{Content: core.Content{core.TextBlock{Text: "hi"}}}},
	})
	for range s.Events() {
	}
	msg := s.Result()
	if seen == nil {
		t.Fatal("the request did not pass through the SDK client")
	}
	if seen.Method != http.MethodPost || !strings.HasSuffix(seen.URL.Path, "/v1/messages") {
		t.Fatalf("SDK request = %s %s", seen.Method, seen.URL)
	}
	if seen.Header.Get("X-Api-Key") != "sk-ant-test" {
		t.Fatal("the SDK client's own credential was not used")
	}
	if msg == nil || msg.StopReason == core.StopReasonError {
		t.Fatalf("the SDK stream was not decoded: %+v", msg)
	}

	for _, gone := range []string{"../sse.go", "../transport.go", "../call.go"} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("provider/%s still exists", filepath.Base(gone))
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, hand := range []string{"NewSSEReader", "provider.Call{", "ResolveAuthWith", "provider.Credentials"} {
			if strings.Contains(string(b), hand) {
				t.Errorf("%s still uses the hand-rolled %s", f, hand)
			}
		}
	}
}
