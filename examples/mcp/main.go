// Command mcp is AgentKit as a Model Context Protocol CLIENT: it borrows
// another program's tools and hands them to a model.
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/mcp "which topics do you know, and what do you say about qualified names?"
//	go run ./examples/mcp --external "npx -y @modelcontextprotocol/server-github"
//
// --external takes any stdio MCP server.
//
// It needs nothing installed. It starts an MCP server inside this process,
// built directly on the official SDK, and talks to it over a pipe with the
// shipped client — so what runs is the real qualified-name path and not a
// stand-in that merely has two underscores in its name. AgentKit ships no
// server of its own.
//
// The protocol is the official Go SDK's (github.com/modelcontextprotocol/go-sdk),
// which negotiates: it speaks 2026-07-28 to a server that has migrated and
// falls back to the initialize handshake of an earlier revision for one that
// has not.
//
// See examples/README.md for the full environment-variable table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/catalog"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/mcp"
	"github.com/agentfox/agentkit-go/provider"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	external := flag.String("external", "",
		"also connect to a real MCP server, given as a command line to spawn")
	flag.Parse()

	// A signal-aware context tears the pool's subprocesses down on the way
	// out. A leaked MCP server is a leaked process tree.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		prompt = "List the topics you can search, then tell me what the docs say about qualified names."
	}
	return clientMode(ctx, prompt, *external)
}

// ------------------------------------------------------------------ client

func clientMode(ctx context.Context, prompt, external string) error {
	// 1. The pool's options carry the wire limits and the warning hook.
	//    ConnectionOptions are per-connection, so building one value and
	//    passing it to both the pool and the hand-made connection is what keeps
	//    a directly-attached server from being the one that warns about nothing.
	opts := mcp.ConnectionOptions{
		ClientInfo: mcp.Implementation{Name: "agentkit-example-mcp", Version: "0.1.0"},
		Limits:     wire.Defaults(),
		Warnf:      stderrf("mcp"),
	}
	pool := mcp.NewPool(opts)

	// 2. NativeTools is set BEFORE any connection is opened, and this is the
	//    footgun the example exists for. A server is free to call its tool
	//    `read_file`, and a config that turns the `<name>__` prefix off — or
	//    replaces it with one that collides — puts that tool in front of
	//    yours. The model then calls `read_file` and a server answers: not an
	//    error, just the wrong tool having run. Declared here, the same
	//    misconfiguration is a refused connection at startup instead.
	native := []core.Tool{wordCountTool()}
	pool.NativeTools = toolNames(native)

	// 3. Something to connect to. A pipe transport joins a client and a server
	//    in one process with no subprocess, no port and no timing, which is
	//    what makes this example runnable with nothing installed. Everything
	//    else is identical for a server spawned as a subprocess — only the
	//    transport differs. Connecting probes the server (and negotiates the
	//    protocol version) up front, so a server that cannot be talked to is
	//    reported now rather than in the middle of a turn the user is paying
	//    for.
	conn, stopServer, err := connectOverPipe(ctx, docsServer(), mcp.ServerConfig{Name: "docs"}, opts)
	if err != nil {
		return err
	}
	defer stopServer()
	if err := pool.Add(conn); err != nil {
		return err
	}
	defer pool.Close()

	if external != "" {
		if err := connectExternal(ctx, pool, external); err != nil {
			return err
		}
	}

	// 4. Tools adapts every connected server into core.Tools whose names are
	//    QUALIFIED: `docs__search_docs`, not `search_docs`. The qualified name
	//    is not cosmetic. It is the name the allowlist matches and the name
	//    BeforeToolCall is handed. A gate written against the unqualified name
	//    does not merely fail to match — it fails OPEN: the policy never fires
	//    and the call goes through.
	mcpTools, err := pool.Tools(ctx, native)
	if err != nil {
		return err
	}
	fmt.Printf("connected servers: %v\n", pool.Names())
	fmt.Printf("tools discovered over MCP: %v\n", toolNames(mcpTools))
	// A server's malformed outputSchema does not cost the tool, only its
	// output typing; the pool reports it rather than failing the connection.
	for _, d := range pool.Diagnostics() {
		fmt.Printf("  ! %s\n", d)
	}

	// 5. Prove the MCP round trip before spending a token on it. This runs the
	//    adapted tool exactly as the loop would.
	if err := smokeCall(ctx, mcpTools, "docs__list_topics"); err != nil {
		return err
	}

	demoShadowedNameIsRefused(ctx, pool.NativeTools)

	// 6. From here it is an ordinary agent. The MCP tools are core.Tools like
	//    any other, which is the point of the adaptation: nothing downstream
	//    knows or cares that a subprocess is behind one of them.
	model, err := catalog.ResolveModel(modelSpec())
	if err != nil {
		return err
	}
	cfg := core.AgentConfig{Model: model}
	agentkit.RegisterDefaults(&cfg,
		anthropic.Provider(anthropic.Options{}),
	)
	cfg.StopPolicy = func(sc core.StopContext) bool {
		switch {
		case sc.TurnCount >= 8:
			sc.SetReason(core.RunStopMaxTurns)
			return true
		case sc.Usage.CostUSD > 1.00: // dollars, cumulative for the run
			sc.SetReason(core.RunStopBudgetExceeded)
			return true
		}
		return false
	}
	cfg.SystemPrompt = "You are concise. Use the tools rather than guessing, and answer in plain prose."

	// The allowlist is written in QUALIFIED names, and the native tool has to
	// be named too: a non-nil ToolNames is an allowlist over the whole set,
	// custom and built-in alike, not a filter over MCP tools only.
	all := append(append([]core.Tool{}, native...), mcpTools...)
	cfg.ToolPolicy.ToolNames = toolNames(all)

	if err := checkCredentials(model); err != nil {
		return err
	}
	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return err
	}
	for _, t := range all {
		if err := agent.RegisterTool(t); err != nil {
			return err
		}
	}

	// 7. Stream, so a tool call is visible as it happens rather than only in
	//    the transcript afterwards.
	stream, err := agent.Stream(ctx, prompt)
	if err != nil {
		return err
	}
	for event := range stream.Events() {
		switch e := event.(type) {
		case core.TextDeltaEvent:
			fmt.Print(e.Delta)
		case core.ToolCallStartEvent:
			fmt.Printf("\n[calling %s]\n", e.Name)
		case core.ToolExecutionEndEvent:
			status := "ok"
			if e.IsError {
				status = "error"
			}
			fmt.Printf("[%s: %s, %dms]\n", e.Name, status, e.ElapsedMS)
		case core.ErrorEvent:
			fmt.Fprintf(os.Stderr, "\n[stream error: %s]\n", e.Message)
		}
	}
	res, err := stream.RunResult()
	if errors.Is(err, core.ErrAborted) || errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "\n[aborted]")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n[%s · %d turns · $%.5f]\n", model.ID, res.TurnCount, res.Usage.CostUSD)
	return nil
}

// smokeCall runs one adapted MCP tool directly, the way the loop would.
//
// It is here because the two halves of this program fail differently: an MCP
// problem is a transport or a name problem and shows up now, while a model
// problem shows up several seconds and one credential later. Separating them
// costs one round trip over a pipe.
func smokeCall(ctx context.Context, tools []core.Tool, qualified string) error {
	for _, t := range tools {
		if t.Name != qualified {
			continue
		}
		res := t.Execute(ctx, json.RawMessage(`{}`))
		body, _ := json.Marshal(res.Data)
		fmt.Printf("direct call to %s: ok=%t %s\n", qualified, res.OK, truncate(string(body), 160))
		return nil
	}
	return fmt.Errorf("the pool did not expose %q; the qualified name is the server name plus %q",
		qualified, "__")
}

// demoShadowedNameIsRefused makes REQ-MCP-CLIENT-06 concrete.
//
// The collision needs a server whose tool lands on a name we already own, and
// with the default `<name>__` prefix that cannot happen — which is exactly why
// the prefix exists. DisablePrefix is the configuration that removes the
// guard, so that is what this connects with. The pool refuses rather than
// letting the server's `word_count` stand in front of ours.
func demoShadowedNameIsRefused(ctx context.Context, native []string) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "helper", Version: "0.1.0"}, nil)
	addTool(srv, &mcp.Tool{Name: "word_count", Description: "a server's idea of word_count"},
		func(map[string]any) *mcp.CallToolResult { return textResult("999") })

	opts := mcp.ConnectionOptions{Limits: wire.Defaults()}
	conn, stopServer, err := connectOverPipe(ctx, srv,
		mcp.ServerConfig{Name: "helper", DisablePrefix: true}, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "shadow demo:", err)
		return
	}
	defer stopServer()

	pool := mcp.NewPool(opts)
	pool.NativeTools = native
	defer pool.Close()
	if err := pool.Add(conn); err != nil {
		fmt.Fprintln(os.Stderr, "shadow demo:", err)
		return
	}
	if _, err := pool.Tools(ctx, nil); err != nil {
		fmt.Printf("shadowing refused: %v\n", err)
		return
	}
	fmt.Println("shadowing was NOT refused, which is a bug in this example or in the pool")
}

// connectExternal spawns a real MCP server, the way an application does.
//
// Two things here are not decoration. The child gets a REDUCED environment
// (REQ-MCP-CLIENT-10) — not os.Environ(), which would hand every credential
// this process holds to a program the user found on npm — and the credential
// it does need travels as a `${VAR}` reference resolved at spawn time, so the
// token is in neither the config file nor the process table.
//
// An unresolved reference is an *mcp.UnresolvedVariableError and nothing is
// spawned. That is deliberate: substituting a blank would start the server
// with an empty credential, and the 401 that follows names the token rather
// than the variable nobody set.
func connectExternal(ctx context.Context, pool *mcp.Pool, cmdline string) error {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return errors.New("--external needs a command")
	}
	cfg := mcp.ServerConfig{
		Name:    "github",
		Command: fields[0],
		Args:    fields[1:],
		Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}"},
		Timeout: 30 * time.Second,
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}

	// The secrets function is where a real application reaches into its own
	// store — a keychain, Vault, a sealed file. os.Getenv stands in for one.
	_, err := pool.Connect(ctx, cfg, env, os.Getenv)
	var unresolved *mcp.UnresolvedVariableError
	if errors.As(err, &unresolved) {
		return fmt.Errorf("%w (set it, or drop the reference from the server config)", err)
	}
	return err
}

// docsServer is the server the client connects to in-process: the official
// SDK's server holding two tools. Anything that speaks MCP would do.
func docsServer() *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "agentkit-example-docs", Version: "0.1.0"},
		&sdk.ServerOptions{Instructions: "Search a small set of notes about how AgentKit wires up MCP."})

	// An MCP tool's input schema is raw JSON Schema on the wire, so it is
	// written as JSON here. A core.Tool's schema is a value built from
	// combinators instead, because that one is rewritten per provider and used
	// to coerce what comes back — see wordCountTool below for the contrast.
	addTool(srv, &mcp.Tool{
		Name:        "list_topics",
		Description: "List the documentation topics available to search",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, func(map[string]any) *mcp.CallToolResult {
		return textResult(strings.Join(topics(), ", "))
	})

	addTool(srv, &mcp.Tool{
		Name:        "search_docs",
		Description: "Search the documentation notes for a word or phrase",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"query": {"type": "string", "description": "words to look for"}},
			"required": ["query"]
		}`),
	}, func(args map[string]any) *mcp.CallToolResult {
		query, _ := args["query"].(string)
		hits := search(query)
		if len(hits) == 0 {
			// A tool that found nothing has not failed. IsError is for a tool
			// that could not run, and the model reacts differently to the two.
			return textResult("no topic matched " + query)
		}
		return textResult(strings.Join(hits, "\n\n"))
	})
	return srv
}

// addTool registers a tool whose handler takes its arguments decoded.
func addTool(srv *sdk.Server, t *mcp.Tool, h func(args map[string]any) *mcp.CallToolResult) {
	if t.InputSchema == nil {
		t.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	srv.AddTool(t, func(_ context.Context, req *sdk.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return &mcp.CallToolResult{IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "arguments: " + err.Error()}}}, nil
			}
		}
		return h(args), nil
	})
}

// connectOverPipe runs a server on one end of a pair of pipes and connects a
// client to the other, returning the connection and a shutdown for the server.
func connectOverPipe(ctx context.Context, srv *sdk.Server, cfg mcp.ServerConfig, opts mcp.ConnectionOptions) (*mcp.ServerConnection, func(), error) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	ss, err := srv.Connect(ctx, mcp.NewPipeTransport(c2sR, s2cW, wire.Defaults()), nil)
	if err != nil {
		return nil, nil, err
	}
	conn, err := mcp.Connect(ctx, cfg, mcp.NewPipeTransport(s2cR, c2sW, wire.Defaults()), opts)
	if err != nil {
		_ = ss.Close()
		return nil, nil, err
	}
	return conn, func() { _ = ss.Close() }, nil
}

// ------------------------------------------------------------------- tools

// wordCountTool is one of OUR tools — the kind NativeTools protects. Its
// schema is built from combinators rather than written as JSON, which is the
// difference between a core.Tool and an mcp.Tool.
func wordCountTool() core.Tool {
	return core.Tool{
		Name:        "word_count",
		Description: "Count the words in a piece of text",
		InputSchema: schema.Object(
			schema.Prop("text", schema.String("The text to count")),
		),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			return core.OKResult(map[string]any{"words": len(strings.Fields(args.Text))})
		},
	}
}

// docs is the corpus. Small on purpose: the example is about the wiring.
var docs = map[string]string{
	"qualified names": "An MCP tool reaches the agent as `<server>__<tool>`. That name is what " +
		"the allowlist and the permission callback both match on.",
	"protocol version": "Revision 2026-07-28 has no handshake: every request carries the version " +
		"and the client's capabilities in its own _meta. A server that has not migrated is reached " +
		"through the initialize handshake of an earlier revision, negotiated automatically.",
	"pipe transport": "A pipe transport joins a client and a server in one process. Every frame " +
		"crossing it is bounded and checked for duplicate keys before it is decoded.",
	"environment": "A server spawned as a subprocess gets a reduced environment, and a credential " +
		"it needs travels as a ${VAR} reference resolved at spawn time.",
}

func topics() []string {
	out := make([]string, 0, len(docs))
	for k := range docs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func search(query string) []string {
	var out []string
	for _, topic := range topics() {
		body := strings.ToLower(topic + " " + docs[topic])
		for _, word := range strings.Fields(strings.ToLower(query)) {
			if len(word) > 3 && strings.Contains(body, word) {
				out = append(out, topic+": "+docs[topic])
				break
			}
		}
	}
	return out
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// ----------------------------------------------------------------- helpers

func stderrf(tag string) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "["+tag+"] "+format+"\n", args...)
	}
}

func toolNames(ts []core.Tool) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// modelSpec is "vendor/model-id", or a bare id when it is unambiguous.
func modelSpec() string {
	if s := os.Getenv("AGENTKIT_MODEL"); s != "" {
		return s
	}
	return "anthropic/claude-sonnet-5"
}

// checkCredentials fails BEFORE the request with a message naming the variable
// to set, rather than after a 401 that names none of them.
//
// The three-state check matters: a deployment using an instance role or ADC
// has no key this process can read and a transport that will nonetheless
// authenticate, so "ambient" must pass a pre-flight that "none" fails.
func checkCredentials(m *core.Model) error {
	auth := provider.ResolveAuth(anthropic.VendorAuth, provider.Env{})
	if auth.State != provider.CredentialNone {
		return nil
	}
	return fmt.Errorf("no credential for vendor %q: set one of %s (see examples/README.md)",
		m.Provider, strings.Join(varNames(anthropic.VendorAuth), ", "))
}

func varNames(v provider.VendorAuth) []string {
	out := make([]string, 0, len(v.Vars)+1)
	for _, e := range v.Vars {
		out = append(out, e.Name)
	}
	if v.BaseURLVar != "" {
		out = append(out, v.BaseURLVar+" (for a gateway or a local server)")
	}
	if len(out) == 0 {
		out = append(out, "a vendor-specific API key")
	}
	return out
}
