package mcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/agentfox/agentkit-go/internal/diag"
	"github.com/agentfox/agentkit-go/internal/toml"
)

// Diagnostic is the shared non-fatal report.
type Diagnostic = diag.Diagnostic

// Config is the `[mcp]` section (REQ-MCP-CLIENT-07).
type Config struct {
	Servers []ServerConfig
}

// ParseConfig reads the `[mcp]` section.
//
// Config is LOCALLY AUTHORED, so it decodes leniently (REQ-SEC-12.5): an
// unknown key is a diagnostic. The opposite of the wire package's rule, and
// the difference is who wrote the bytes.
func ParseConfig(path string, src []byte) (Config, []Diagnostic, error) {
	root, diags, err := toml.ParseTOML(src)
	if err != nil {
		return Config{}, diags, fmt.Errorf("mcp: %s: %w", path, err)
	}

	var cfg Config
	if mcpTbl, ok := root.Sub("mcp"); ok {
		servers, _ := mcpTbl.Array("servers")
		seen := map[string]bool{}
		for i, st := range servers {
			sc, sdiags := parseServer(path, i, st)
			diags = append(diags, sdiags...)
			if sc.Name == "" {
				continue
			}
			if seen[sc.Name] {
				diags = append(diags, Diagnostic{Path: path, Severity: diag.SeverityError,
					Message: fmt.Sprintf("two [[mcp.servers]] entries are named %q; the "+
						"name keys the pool and the tool prefix", sc.Name)})
				continue
			}
			seen[sc.Name] = true
			cfg.Servers = append(cfg.Servers, sc)
		}
	}

	return cfg, diags, nil
}

func parseServer(path string, i int, t *toml.Table) (ServerConfig, []Diagnostic) {
	var (
		sc    ServerConfig
		diags []Diagnostic
	)
	where := fmt.Sprintf("[[mcp.servers]] #%d", i+1)

	str := func(key string) string {
		if v, ok := t.Get(key); ok && v.Kind == toml.KindString {
			return v.Str
		}
		return ""
	}

	sc.Name = strings.TrimSpace(str("name"))
	sc.Command = str("command")
	sc.URL = str("url")
	sc.Dir = str("dir")
	sc.ToolPrefix = str("tool_prefix")

	if v, ok := t.Get("args"); ok && v.Kind == toml.KindStringArray {
		sc.Args = append([]string(nil), v.Array...)
	}
	if v, ok := t.Get("allow_sampling"); ok && v.Kind == toml.KindBool {
		sc.AllowSampling = v.Bool
	}
	if v, ok := t.Get("per_session_call_limit"); ok && v.Kind == toml.KindInt {
		sc.PerSessionCallLimit = int(v.Int)
	}
	if v, ok := t.Get("per_session_reconnect_limit"); ok && v.Kind == toml.KindInt {
		sc.PerSessionReconnectLimit = int(v.Int)
	}
	// Int or float: REQ-MCP-CLIENT-07 writes the default as `30.0`, and a
	// parser that accepted only `30` rejected the requirement's own example.
	if v, ok := t.Get("timeout_s"); ok {
		switch v.Kind {
		case toml.KindInt:
			sc.Timeout = time.Duration(v.Int) * time.Second
		case toml.KindFloat:
			sc.Timeout = time.Duration(v.Float * float64(time.Second))
		}
	}
	if hdrTbl, ok := t.Sub("headers"); ok {
		sc.Headers = map[string]string{}
		for _, k := range hdrTbl.Keys() {
			if v, ok := hdrTbl.Get(k); ok && v.Kind == toml.KindString {
				sc.Headers[k] = v.Str
			}
		}
	}
	if envTbl, ok := t.Sub("env"); ok {
		sc.Env = map[string]string{}
		for _, k := range envTbl.Keys() {
			if v, ok := envTbl.Get(k); ok && v.Kind == toml.KindString {
				sc.Env[k] = v.Str
			}
		}
	}

	switch {
	case sc.Name == "":
		diags = append(diags, Diagnostic{Path: path, Severity: diag.SeverityError,
			Message: where + ": name is required"})
	case sc.Command == "" && sc.URL == "":
		diags = append(diags, Diagnostic{Path: path, Severity: diag.SeverityError,
			Message: fmt.Sprintf("%s (%q): needs a command or a url", where, sc.Name)})
		sc.Name = "" // not usable
	case sc.Command != "" && sc.URL != "":
		diags = append(diags, Diagnostic{Path: path, Severity: diag.SeverityWarning,
			Message: fmt.Sprintf("%s (%q): both command and url are set; the command wins "+
				"and the url is ignored", where, sc.Name)})
	}

	// An unknown transport is an error, not a fallback: silently serving
	// Streamable HTTP to an operator who asked for something else would hide
	// that their server is probably unreachable.
	switch sc.Transport = str("transport"); sc.Transport {
	case "", "streamable-http", "sse":
	default:
		diags = append(diags, Diagnostic{Path: path, Severity: diag.SeverityError,
			Message: fmt.Sprintf("%s (%q): transport %q is not implemented; use "+
				"\"streamable-http\" (the default) or \"sse\"", where, sc.Name, sc.Transport)})
		sc.Name = "" // not usable
	}
	return sc, diags
}
