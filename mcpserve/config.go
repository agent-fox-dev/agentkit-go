package mcpserve

import "encoding/json"

// defaultServerName is the name a harness knows the server by when the caller
// gives none.
const defaultServerName = "agentkit"

// ClaudeConfig is the JSON Claude Code's --mcp-config takes to connect to this
// endpoint as the MCP server called name (empty means "agentkit"): a server of
// type http, with the endpoint's URL and its bearer token in the Authorization
// header.
//
// The output is built with encoding/json, so any name is escaped correctly, and
// it reflects the Endpoint's URL and Token as they are when it is called.
func (e *Endpoint) ClaudeConfig(name string) string { return e.config(name, false) }

// PiConfig is the JSON Pi's .pi/mcp.json takes: ClaudeConfig's object with
// "exposure":"direct" in the server entry, so Pi declares the tools to its model
// directly instead of reaching them only through its own code mode. Pi reads a
// project's .pi/mcp.json only once the project is trusted, and its print mode
// cannot ask: run it with --approve, or it starts with no server.
func (e *Endpoint) PiConfig(name string) string { return e.config(name, true) }

func (e *Endpoint) config(name string, pi bool) string {
	if name == "" {
		name = defaultServerName
	}
	server := map[string]any{
		"type":    "http",
		"url":     e.URL,
		"headers": map[string]string{"Authorization": "Bearer " + e.Token},
	}
	if pi {
		server["exposure"] = "direct"
	}
	// Strings, maps of strings and a bool always marshal.
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{name: server}})
	return string(b)
}
