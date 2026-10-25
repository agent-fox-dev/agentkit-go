package mcp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/diag"
)

func TestParseConfigReadsEveryServerKey(t *testing.T) {
	src := `
[mcp]

[[mcp.servers]]
name = " github "
command = "gh-mcp"
args = ["--stdio"]
dir = "/srv"
tool_prefix = "gh."
allow_sampling = true
per_session_call_limit = 25
per_session_reconnect_limit = 5
timeout_s = 10

[mcp.servers.env]
GITHUB_TOKEN = "${GH_PAT}"

[[mcp.servers]]
name = "db"
url = "https://db.example/mcp"
transport = "sse"
timeout_s = 2.5

[mcp.servers.headers]
Authorization = "Bearer ${DB_TOKEN}"
`
	cfg, diags, err := ParseConfig("config.toml", []byte(src))
	must(t, err)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	want := []ServerConfig{
		{Name: "github", Command: "gh-mcp", Args: []string{"--stdio"}, Dir: "/srv", ToolPrefix: "gh.",
			AllowSampling: true, PerSessionCallLimit: 25, PerSessionReconnectLimit: 5,
			Timeout: 10 * time.Second, Env: map[string]string{"GITHUB_TOKEN": "${GH_PAT}"}},
		{Name: "db", URL: "https://db.example/mcp", Transport: "sse", Timeout: 2500 * time.Millisecond,
			Headers: map[string]string{"Authorization": "Bearer ${DB_TOKEN}"}},
	}
	// The ${VAR} references survive parsing: they are resolved at connect
	// time, so a credential never sits in the config file.
	if !reflect.DeepEqual(cfg.Servers, want) {
		t.Fatalf("servers =\n%+v\nwant\n%+v", cfg.Servers, want)
	}
}

func TestParseConfigReportsUnusableServers(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		severity  diag.Severity
		message   string
		kept      []string
	}{
		{"no name", `command = "a"`, diag.SeverityError, "name is required", nil},
		{"neither command nor url", `name = "x"`, diag.SeverityError, "needs a command or a url", nil},
		{"both command and url", "name = \"x\"\ncommand = \"a\"\nurl = \"http://h\"", diag.SeverityWarning, "the command wins", []string{"x"}},
		{"unknown transport", "name = \"x\"\nurl = \"http://h\"\ntransport = \"websocket\"", diag.SeverityError, `"websocket" is not implemented`, nil},
		{"duplicate name", "name = \"x\"\ncommand = \"a\"\n\n[[mcp.servers]]\nname = \"x\"\ncommand = \"b\"", diag.SeverityError, "two [[mcp.servers]] entries", []string{"x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, diags, err := ParseConfig("c.toml", []byte("[[mcp.servers]]\n"+tc.src+"\n"))
			must(t, err)
			if len(diags) != 1 || diags[0].Severity != tc.severity || !strings.Contains(diags[0].Message, tc.message) ||
				diags[0].Path != "c.toml" {
				t.Fatalf("diagnostics = %v, want one %s containing %q", diags, tc.severity, tc.message)
			}
			var names []string
			for _, s := range cfg.Servers {
				names = append(names, s.Name)
			}
			if !reflect.DeepEqual(names, tc.kept) {
				t.Fatalf("servers kept = %v, want %v", names, tc.kept)
			}
		})
	}
}

func TestParseConfigWithoutAnMCPSectionIsEmpty(t *testing.T) {
	cfg, diags, err := ParseConfig("c.toml", []byte("[other]\nkey = 1\n"))
	if err != nil || len(diags) != 0 || len(cfg.Servers) != 0 {
		t.Fatalf("cfg = %+v, diags = %v, err = %v", cfg, diags, err)
	}
}

func TestParseConfigRejectsInvalidTOMLNamingTheFile(t *testing.T) {
	_, _, err := ParseConfig("broken.toml", []byte("[[mcp.servers]\nname = \n"))
	if err == nil || !strings.Contains(err.Error(), "broken.toml") {
		t.Fatalf("err = %v, want a parse error naming the file", err)
	}
}
