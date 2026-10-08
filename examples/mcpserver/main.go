// Command mcpserver is REQ-MCP-SERVER-02's reference driver.
//
// The requirement names `nightshift --mcp-server`; nightshift is a daemon
// built ON this SDK, so the SDK ships the mechanism (mcp.Server, Server.Run)
// and this binary as the smallest complete host. A real host does exactly what
// main does here — construct a Server, register its own tools and resources,
// and call Run — with its own inventory in place of the demonstration one.
//
// The tools registered below exist to make the binary runnable end to end
// against a real MCP client; they are not part of the SDK's surface.
//
//	go run ./examples/mcpserver                                # stdio
//	go run ./examples/mcpserver -config agentkit.toml          # whatever [mcp_server] selects
//	go run ./examples/mcpserver -transport http -port 8722 -api-key-env MCP_API_KEY
//
// Exit 0 on a clean shutdown, 1 on a startup or transport failure.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/agentfox/agentkit-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	var (
		configPath = flag.String("config", "", "TOML file whose [mcp_server] table selects the mode")
		transport  = flag.String("transport", "", `"stdio" or "http" (overrides the config)`)
		port       = flag.Int("port", 0, "TCP port for http mode (overrides the config)")
		apiKeyEnv  = flag.String("api-key-env", "", "environment variable holding the http API key")
	)
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fail(err)
	}
	// Flags win over the file: an operator debugging a deployment should not
	// have to edit the config they are trying to reproduce.
	if *transport != "" {
		cfg.Transport, cfg.Enabled = *transport, true
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *apiKeyEnv != "" {
		cfg.APIKeyEnv = *apiKeyEnv
	}
	if !cfg.Enabled && *configPath == "" {
		// Invoked with no config and no flags. Server mode is off by default
		// (REQ-MCP-SERVER-01), but a binary whose entire purpose is to serve
		// and that silently exits 0 looks like a crash; stdio is the safe mode
		// to assume, since it needs no port and no credential.
		cfg.Enabled, cfg.Transport = true, "stdio"
	}

	// Diagnostics go to STDERR, always. In stdio mode stdout carries the
	// protocol and a stray line there is a frame the client's decoder is
	// poisoned by.
	srv := mcp.NewServer(mcp.ServerOptions{
		Info:   mcp.Implementation{Name: "agentkit-mcp-server", Version: "0.1.0"},
		Limits: mcp.DefaultLimits(),
		Instructions: "A reference AgentKit MCP server. Tools here are for " +
			"demonstration; a host registers its own.",
	})
	if err := registerDemo(srv); err != nil {
		fail(err)
	}

	// SIGINT/SIGTERM cancel the context, which stops the listener and cancels
	// every in-flight handler.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx, cfg, os.Getenv); err != nil {
		fail(err)
	}
}

func loadConfig(path string) (mcp.ServerModeConfig, error) {
	if path == "" {
		return mcp.ServerModeConfig{}, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return mcp.ServerModeConfig{}, err
	}
	cfg, diags, err := mcp.ParseConfig(path, src)
	for _, d := range diags {
		fmt.Fprintln(os.Stderr, "mcp-server: "+d.String())
	}
	if err != nil {
		return mcp.ServerModeConfig{}, err
	}
	return cfg.Server, nil
}

// registerDemo is the part a real host replaces.
func registerDemo(s *mcp.Server) error {
	if err := s.RegisterTool(&mcp.Tool{
		Name:        "echo",
		Description: "Return the supplied message unchanged.",
		InputSchema: json.RawMessage(
			`{"type":"object","properties":{"message":{"type":"string","description":"text to return"}},"required":["message"]}`),
	}, func(_ context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		msg, ok := args["message"].(string)
		if !ok {
			return nil, fmt.Errorf("message must be a string")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: msg}}}, nil
	}); err != nil {
		return err
	}

	// Resources are registered through the SDK's own API, which *mcp.Server
	// embeds.
	started := time.Now().UTC().Format(time.RFC3339)
	s.AddResource(&sdk.Resource{
		URI: "agentkit://server/info", Name: "server info", MIMEType: "application/json",
	}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		body, err := json.Marshal(map[string]any{"started": started})
		if err != nil {
			return nil, err
		}
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
			URI: "agentkit://server/info", MIMEType: "application/json", Text: string(body),
		}}}, nil
	})

	// A template, so the reference host exercises the parameterised path too.
	s.AddResourceTemplate(&sdk.ResourceTemplate{
		URITemplate: "agentkit://echo/{message}",
		Name:        "echoed message",
		Description: "Reads back whatever is in the URI.",
	}, func(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		uri := req.Params.URI
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
			URI: uri, MIMEType: "text/plain", Text: strings.TrimPrefix(uri, "agentkit://echo/"),
		}}}, nil
	})
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mcp-server:", err)
	os.Exit(1)
}
