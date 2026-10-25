package mcpserve

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestConfigsNameTheServerAndCarryTheURLAndToken(t *testing.T) {
	for _, c := range []struct{ name, url, token, served string }{
		{"agentkit", "http://127.0.0.1:1234/mcp", "tok", "agentkit"},
		{"", "u", "t", "agentkit"},
		{"odd \"name\"\\\n\t日本 {}", `"url"<>&` + "\x00", "tok\x1fen'", "odd \"name\"\\\n\t日本 {}"},
	} {
		ep := &Endpoint{URL: c.url, Token: c.token}
		entry := map[string]any{"type": "http", "url": c.url, "headers": map[string]string{"Authorization": "Bearer " + c.token}}
		claude := testutil.MustJSON(t, map[string]any{"mcpServers": map[string]any{c.served: entry}})
		entry["exposure"] = "direct"
		pi := testutil.MustJSON(t, map[string]any{"mcpServers": map[string]any{c.served: entry}})
		if got := ep.ClaudeConfig(c.name); !testutil.SameJSON([]byte(got), []byte(claude)) {
			t.Errorf("ClaudeConfig(%q) = %s, want %s", c.name, got, claude)
		}
		if got := ep.PiConfig(c.name); !testutil.SameJSON([]byte(got), []byte(pi)) {
			t.Errorf("PiConfig(%q) = %s, want %s", c.name, got, pi)
		}
	}
}
