package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/agentfox/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The protocol types a host meets through this package's API, re-exported so
// a host does not have to import two packages both named mcp. Everything else
// is the SDK's (github.com/modelcontextprotocol/go-sdk/mcp).
type (
	Implementation      = sdk.Implementation
	Tool                = sdk.Tool
	Content             = sdk.Content
	TextContent         = sdk.TextContent
	CallToolResult      = sdk.CallToolResult
	CreateMessageParams = sdk.CreateMessageParams
	CreateMessageResult = sdk.CreateMessageResult
)

// ResultCharCap is REQ-MCP-CLIENT-09: 50K characters per tool result.
//
// It is counted in RUNES, not bytes. "Characters" in a requirement about model
// context means what the model sees, and a byte cap truncates a CJK result to
// a third of the length an ASCII one gets.
const ResultCharCap = 50_000

// DefaultCallLimit and DefaultTimeout are REQ-MCP-CLIENT-07's defaults;
// DefaultReconnectLimit is NFR-REL-03's.
const (
	DefaultCallLimit      = 1000
	DefaultTimeout        = 30 * time.Second
	DefaultReconnectLimit = 3
)

// Errors a connection can raise.
var (
	// ErrNameCollision is REQ-MCP-CLIENT-06's MCPNameCollisionError, raised at
	// CONNECTION time rather than call time.
	ErrNameCollision = errors.New("mcp: tool name collides with an existing tool")
	// ErrCallLimit is REQ-MCP-CLIENT-07's per-session cap (REQ-SEC-08).
	ErrCallLimit = errors.New("mcp: per-session call limit reached")
	// ErrSamplingNotAllowed is REQ-MCP-CLIENT-08's gate.
	ErrSamplingNotAllowed = errors.New("mcp: sampling is not enabled for this server")
	// ErrReconnectLimit is NFR-REL-03's bound: the transport died more times
	// than per_session_reconnect_limit allows, and the connection is dead for
	// the rest of the session. Every later call fails with it immediately.
	ErrReconnectLimit = errors.New("mcp: per-session reconnect limit reached; the connection is dead")
	// ErrClosed is returned by a connection after Close.
	ErrClosed = errors.New("mcp: connection is closed")
)

// ServerConfig is one `[[mcp.servers]]` entry (REQ-MCP-CLIENT-07).
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	// URL selects a remote server. Transport picks the HTTP binding:
	// "streamable-http" (the default) or "sse", the 2024-11-05 HTTP+SSE
	// transport. Protocol versions are negotiated either way. A url server
	// is not subscribed to tools/list_changed — that is a request held open
	// for the life of the session against a remote; a command server is
	// (REQ-CACHE-07).
	URL       string
	Transport string
	Dir       string
	// Headers are sent on every request to a URL server. Values may carry
	// ${VAR} references, resolved from the secrets store exactly like Env.
	Headers map[string]string
	// Env values may carry ${VAR} references, resolved at spawn time against
	// the secrets store (REQ-MCP-CLIENT-10).
	Env map[string]string
	// ToolPrefix overrides the default `<name>__`. An empty string in the
	// config file means the default; DisablePrefix is how a caller asks for
	// none, because "" cannot mean both.
	ToolPrefix    string
	DisablePrefix bool
	AllowSampling bool
	// PerSessionCallLimit is 0 for the default. A NEGATIVE value disables the
	// limit, so "unlimited" is something a caller has to write down.
	PerSessionCallLimit int
	// PerSessionReconnectLimit is NFR-REL-03's bound on re-spawning (stdio) or
	// re-opening (HTTP) a transport that died, per session. 0 means
	// DefaultReconnectLimit; a NEGATIVE value means never reconnect.
	PerSessionReconnectLimit int
	Timeout                  time.Duration
}

func (c ServerConfig) prefix() string {
	if c.DisablePrefix {
		return ""
	}
	if c.ToolPrefix != "" {
		return c.ToolPrefix
	}
	return c.Name + "__"
}

func (c ServerConfig) callLimit() int {
	switch {
	case c.PerSessionCallLimit < 0:
		return -1 // explicitly unlimited
	case c.PerSessionCallLimit == 0:
		return DefaultCallLimit
	}
	return c.PerSessionCallLimit
}

func (c ServerConfig) reconnectLimit() int {
	switch {
	case c.PerSessionReconnectLimit < 0:
		return 0 // explicitly never
	case c.PerSessionReconnectLimit == 0:
		return DefaultReconnectLimit
	}
	return c.PerSessionReconnectLimit
}

func (c ServerConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// SamplingHandler answers a server's sampling request. It is consulted only
// for a server with AllowSampling set.
type SamplingHandler func(ctx context.Context, p *CreateMessageParams) (*CreateMessageResult, error)

// ConnectionOptions are the runtime hooks a connection needs.
type ConnectionOptions struct {
	// Sampling answers server-initiated sampling. Nil refuses.
	Sampling SamplingHandler
	// Warnf reports non-fatal trouble: reconnects, a child's stderr. Nil
	// discards.
	Warnf  func(format string, args ...any)
	Limits wire.Limits
	// ClientInfo identifies us to the server.
	ClientInfo Implementation
}

// ServerConnection is REQ-MCP-CLIENT-03: one server, over an SDK session that
// is replaced when it dies (NFR-REL-03).
type ServerConnection struct {
	cfg    ServerConfig
	opts   ConnectionOptions
	client *sdk.Client
	// dial makes a fresh transport for a reconnect. Nil means a dead session
	// stays dead; Pool.Connect sets it only after the first connect succeeded.
	dial func() sdk.Transport

	mu   sync.Mutex
	sess *sdk.ClientSession
	gone chan struct{} // closed when sess ends
	end  context.CancelFunc
	// reconnects counts attempts, failed dials included, against
	// PerSessionReconnectLimit; deadErr is set once the budget is spent.
	reconnects int
	deadErr    error
	calls      int
	closed     bool
}

// Connect opens a connection over a transport the caller built — an
// in-process pipe, an SDK transport. It is never re-dialled; Pool.Connect is
// the path that reconnects.
func Connect(ctx context.Context, cfg ServerConfig, t sdk.Transport, opts ConnectionOptions) (*ServerConnection, error) {
	c := newConnection(cfg, opts)
	if err := c.open(ctx, t); err != nil {
		return nil, err
	}
	return c, nil
}

func newConnection(cfg ServerConfig, opts ConnectionOptions) *ServerConnection {
	if opts.ClientInfo.Name == "" {
		opts.ClientInfo = Implementation{Name: "agentkit-go", Version: "0.1.0"}
	}
	c := &ServerConnection{cfg: cfg, opts: opts}
	// An empty capability set: no roots. Sampling is advertised only when it
	// will be honoured (REQ-MCP-CLIENT-08), because the SDK advertises it
	// whenever a handler is installed.
	co := &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}}
	if cfg.AllowSampling {
		co.CreateMessageHandler = c.sample
	}
	if cfg.Command != "" {
		// A handler is what makes the SDK hold a tools/list_changed
		// subscription open, which keeps its tool cache honest (REQ-CACHE-07).
		co.ToolListChangedHandler = func(context.Context, *sdk.ToolListChangedRequest) {}
	}
	c.client = sdk.NewClient(&c.opts.ClientInfo, co)
	return c
}

// open connects t under timeout_s.
//
// The session must outlive the caller's context — the SDK runs the
// connection's read loop on the context Connect was given — so Connect gets a
// context of its own, cancelled only by Close or by a handshake that missed
// its deadline. The handshake runs on its own goroutine because the SDK's
// failure path closes gracefully, waiting on a peer that may be the reason it
// failed: timeout_s is a bound on returning, not only on the answer.
func (c *ServerConnection) open(ctx context.Context, t sdk.Transport) error {
	life, end := context.WithCancel(context.WithoutCancel(ctx))
	type result struct {
		sess *sdk.ClientSession
		err  error
	}
	done := make(chan result, 1)
	go func() {
		sess, err := c.client.Connect(life, t, nil)
		done <- result{sess, err}
	}()
	timer := time.NewTimer(c.cfg.timeout())
	defer timer.Stop()

	var err error
	select {
	case r := <-done:
		if r.err == nil {
			gone := make(chan struct{})
			go func() { _ = r.sess.Wait(); close(gone) }()
			c.sess, c.gone, c.end = r.sess, gone, end
			return nil
		}
		end()
		return fmt.Errorf("mcp: %s: connect: %w", c.cfg.Name, r.err)
	case <-timer.C:
		err = fmt.Errorf("%w after %s", context.DeadlineExceeded, c.cfg.timeout())
	case <-ctx.Done():
		err = ctx.Err()
	}
	end()
	go func() {
		if r := <-done; r.sess != nil {
			_ = r.sess.Close()
		}
	}()
	return fmt.Errorf("mcp: %s: connect: %w", c.cfg.Name, err)
}

// Name is the configured server name.
func (c *ServerConnection) Name() string { return c.cfg.Name }

// Session returns the live SDK session, reconnecting first if the last one
// died. It is the way to everything this package does not wrap — resources,
// prompts, the negotiated InitializeResult.
func (c *ServerConnection) Session(ctx context.Context) (*sdk.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.closed:
		return nil, ErrClosed
	case c.deadErr != nil:
		return nil, c.deadErr
	case !isDone(c.gone):
		return c.sess, nil
	case c.dial == nil:
		return nil, fmt.Errorf("mcp: %s: the session ended", c.cfg.Name)
	}
	// Reconnect only BEFORE a request is sent: a call cut off mid-flight has
	// already failed, and re-sending it could run the tool twice.
	limit := c.cfg.reconnectLimit()
	if c.reconnects >= limit {
		c.deadErr = fmt.Errorf("%w: %s died again after %d reconnect(s)",
			ErrReconnectLimit, c.cfg.Name, c.reconnects)
		c.warnf("server %q died and the reconnect limit (%d) is spent; the connection is "+
			"dead for the rest of the session", c.cfg.Name, limit)
		return nil, c.deadErr
	}
	c.reconnects++
	c.warnf("server %q died; reconnecting (%d/%d)", c.cfg.Name, c.reconnects, limit)
	c.end()
	if err := c.open(ctx, c.dial()); err != nil {
		return nil, fmt.Errorf("mcp: %s: reconnect %d/%d: %w", c.cfg.Name, c.reconnects, limit, err)
	}
	return c.sess, nil
}

func isDone(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Alive reports whether the current session is still open.
func (c *ServerConnection) Alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && c.deadErr == nil && !isDone(c.gone)
}

// Reconnects is how many reconnect attempts this session has spent.
func (c *ServerConnection) Reconnects() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconnects
}

// Dead reports whether the reconnect budget is exhausted (NFR-REL-03).
func (c *ServerConnection) Dead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadErr != nil
}

// ListTools lists every tool the server exposes, across pages. The SDK caches
// the list for as long as the server's ttlMs allows, and drops it on
// tools/list_changed.
func (c *ServerConnection) ListTools(ctx context.Context) ([]*Tool, error) {
	sess, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.timeout())
	defer cancel()
	var out []*Tool
	for t, err := range sess.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("mcp: %s: tools/list: %w", c.cfg.Name, err)
		}
		out = append(out, t)
	}
	return out, nil
}

// Call invokes a tool by its UNQUALIFIED name (REQ-MCP-CLIENT-03).
func (c *ServerConnection) Call(ctx context.Context, toolName string, args map[string]any) (*CallToolResult, error) {
	var raw json.RawMessage
	if args != nil {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, fmt.Errorf("mcp: %s: tools/call %q: arguments: %w", c.cfg.Name, toolName, err)
		}
		raw = b
	}
	return c.CallRaw(ctx, toolName, raw)
}

// CallRaw is Call with the arguments as the JSON the caller already holds.
//
// The pool uses it to pass the model's argument bytes through VERBATIM. Going
// through map[string]any would launder every number through a float64:
// 9007199254740993 arrives at the server as 9007199254740992.
func (c *ServerConnection) CallRaw(ctx context.Context, toolName string, args json.RawMessage) (*CallToolResult, error) {
	c.mu.Lock()
	limit := c.cfg.callLimit()
	if limit >= 0 && c.calls >= limit {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %s allows %d calls per session", ErrCallLimit, c.cfg.Name, limit)
	}
	c.calls++
	c.mu.Unlock()

	params := &sdk.CallToolParams{Name: toolName}
	if len(args) > 0 {
		params.Arguments = args
	}
	sess, err := c.Session(ctx)
	var res *CallToolResult
	if err == nil {
		cctx, cancel := context.WithTimeout(ctx, c.cfg.timeout())
		res, err = sess.CallTool(cctx, params)
		cancel()
	}
	if err != nil {
		return nil, fmt.Errorf("mcp: %s: tools/call %q: %w", c.cfg.Name, toolName, err)
	}
	res.Content = CapContent(res.Content)
	return res, nil
}

// sample is the sampling gate (REQ-MCP-CLIENT-08). It is installed only for a
// server with AllowSampling, and refuses unless a handler is set.
func (c *ServerConnection) sample(ctx context.Context, req *sdk.CreateMessageRequest) (*CreateMessageResult, error) {
	if c.opts.Sampling == nil {
		return nil, fmt.Errorf("%w: %s", ErrSamplingNotAllowed, c.cfg.Name)
	}
	return c.opts.Sampling(ctx, req.Params)
}

// CapContent applies REQ-MCP-CLIENT-09's 50K cap across the whole result.
//
// The budget is spent ACROSS the content items, not per item: two hundred
// blocks of 49K each would otherwise pass a per-item cap. A non-text block is
// charged by its encoded size and is never SLICED — half a base64 image is a
// corrupt image — but one that does not fit is dropped whole, and the note
// says so. The note is addressed to the model, which has to decide whether to
// ask for less next time.
func CapContent(items []Content) []Content {
	remaining := ResultCharCap
	out := make([]Content, 0, len(items))
	for i, it := range items {
		text, ok := it.(*TextContent)
		if !ok {
			cost := contentCost(it)
			if cost > remaining {
				return append(out, capNote(len(items)-i))
			}
			remaining -= cost
			out = append(out, it)
			continue
		}
		runes := []rune(text.Text)
		if len(runes) <= remaining {
			remaining -= len(runes)
			out = append(out, it)
			continue
		}
		if remaining > 0 {
			cut := *text
			cut.Text = string(runes[:remaining])
			out = append(out, &cut)
		}
		return append(out, capNote(len(items)-i-1))
	}
	return out
}

// contentCost is a non-text block's charge: the base64 length of an image or
// audio payload, the encoded size of anything else.
func contentCost(c Content) int {
	switch c := c.(type) {
	case *sdk.ImageContent:
		return base64.StdEncoding.EncodedLen(len(c.Data))
	case *sdk.AudioContent:
		return base64.StdEncoding.EncodedLen(len(c.Data))
	}
	b, _ := c.MarshalJSON()
	return len(b)
}

func capNote(dropped int) Content {
	return &TextContent{Text: fmt.Sprintf(
		"\n\n[truncated: this result exceeded the %d-character cap. %d further content "+
			"block(s) were dropped. Narrow the request — a filter, a smaller range, a "+
			"more specific query — rather than retrying it unchanged.]",
		ResultCharCap, dropped)}
}

// Close tears the connection down.
func (c *ServerConnection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	err := c.sess.Close()
	c.end()
	return err
}

func (c *ServerConnection) warnf(format string, args ...any) {
	if c.opts.Warnf != nil {
		c.opts.Warnf(format, args...)
	}
}

// QualifiedName is REQ-MCP-CLIENT-05's `server_name__tool_name`.
func QualifiedName(cfg ServerConfig, tool string) string { return cfg.prefix() + tool }

// SplitQualified is the inverse, given the prefix.
func SplitQualified(cfg ServerConfig, qualified string) (string, bool) {
	p := cfg.prefix()
	if p == "" {
		return qualified, true
	}
	return strings.CutPrefix(qualified, p)
}
