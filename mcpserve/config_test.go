package mcpserve

import (
	"encoding/json"
	"math/rand/v2"
	"reflect"
	"testing"
)

// decodeConfig decodes a config string, failing the test when it is not JSON.
func decodeConfig(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q is not JSON: %v", s, err)
	}
	return m
}

func claudeShape(name, url, token string) map[string]any {
	return map[string]any{"mcpServers": map[string]any{name: map[string]any{
		"type": "http", "url": url, "headers": map[string]any{"Authorization": "Bearer " + token}}}}
}

// TS-23-40: ClaudeConfig returns the mcpServers http entry with the URL and the
// Authorization header. 23-REQ-7.1.
func TestClaudeConfig_TS23_40(t *testing.T) {
	ep := &Endpoint{URL: "http://127.0.0.1:1234/mcp", Token: "tok"}
	if got, want := decodeConfig(t, ep.ClaudeConfig("agentkit")), claudeShape("agentkit", ep.URL, "tok"); !reflect.DeepEqual(got, want) {
		t.Errorf("ClaudeConfig = %v, want %v", got, want)
	}
}

// TS-23-41: PiConfig is the ClaudeConfig object plus "exposure":"direct" in the
// server entry, and nothing else different. 23-REQ-7.2.
func TestPiConfig_TS23_41(t *testing.T) {
	ep := &Endpoint{URL: "http://127.0.0.1:1234/mcp", Token: "tok"}
	want := claudeShape("agentkit", ep.URL, "tok")
	want["mcpServers"].(map[string]any)["agentkit"].(map[string]any)["exposure"] = "direct"
	if got := decodeConfig(t, ep.PiConfig("agentkit")); !reflect.DeepEqual(got, want) {
		t.Errorf("PiConfig = %v, want %v", got, want)
	}
}

// TS-23-42: an empty name means "agentkit" in both. 23-REQ-7.3.
func TestEmptyNameMeansAgentkit_TS23_42(t *testing.T) {
	ep := &Endpoint{URL: "u", Token: "t"}
	for name, cfg := range map[string]string{"ClaudeConfig": ep.ClaudeConfig(""), "PiConfig": ep.PiConfig("")} {
		servers, _ := decodeConfig(t, cfg)["mcpServers"].(map[string]any)
		if _, ok := servers["agentkit"]; !ok || len(servers) != 1 {
			t.Errorf("%s(\"\") names %v, want only agentkit", name, servers)
		}
	}
}

// TS-23-43 (property): the output is valid JSON that carries any name, URL and
// token exactly, as of the time of the call. 23-REQ-7.4.
func TestConfigCarriesAnyValues_TS23_43(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 43))
	pool := []rune("ab c\"\\\n\t\x00\x1f/'<>&é✓日本 {}[]:,")
	text := func(min int) string {
		n := min + rng.IntN(12)
		out := make([]rune, n)
		for i := range out {
			out[i] = pool[rng.IntN(len(pool))]
		}
		return string(out)
	}
	ep := &Endpoint{}
	for range 300 {
		name, url, token := text(1), text(0), text(0)
		ep.URL, ep.Token = url, token
		for which, cfg := range map[string]string{"ClaudeConfig": ep.ClaudeConfig(name), "PiConfig": ep.PiConfig(name)} {
			if !json.Valid([]byte(cfg)) {
				t.Fatalf("%s(%q) with URL %q and token %q is not valid JSON: %s", which, name, url, token, cfg)
			}
			servers, _ := decodeConfig(t, cfg)["mcpServers"].(map[string]any)
			entry, ok := servers[name].(map[string]any)
			if !ok || len(servers) != 1 {
				t.Fatalf("%s(%q): the only server is %v", which, name, servers)
			}
			headers, _ := entry["headers"].(map[string]any)
			if entry["type"] != "http" || entry["url"] != url || headers["Authorization"] != "Bearer "+token {
				t.Fatalf("%s(%q): entry %v for URL %q and token %q", which, name, entry, url, token)
			}
			if _, has := entry["exposure"]; has != (which == "PiConfig") {
				t.Fatalf("%s(%q): exposure present is %v", which, name, has)
			}
		}
	}
}
