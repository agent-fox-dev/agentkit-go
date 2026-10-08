package mcp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// §6.8's requirements name nightshift's tools and resources. Those belong to
// the DAEMON, not to a library: AgentKit has no issues and no sessions to
// list. So REQ-MCP-SERVER-03's "explicit tool registration" is what ships
// here, and the host registers its own.

// ToolHandler answers a tools/call registered with RegisterTool. Returning an
// error is a TOOL error the caller's model sees, not a protocol error.
type ToolHandler func(ctx context.Context, args map[string]any) (*CallToolResult, error)

// ServerOptions configures a Server.
type ServerOptions struct {
	Info Implementation
	// Audit receives an event per inbound tool call.
	Audit  func(core.AuditEvent)
	Limits wire.Limits
	// Instructions is the optional free-text hint returned to clients.
	Instructions string
	// PageSize bounds a list page. Zero is the SDK's default.
	PageSize int
}

// Server is the AgentKit MCP server (REQ-MCP-SERVER-01..07): the SDK's
// server, which a host fills with AddTool/AddResource/AddResourceTemplate or
// RegisterTool, plus the policy AgentKit adds on top — tool results capped and
// audited, a panicking handler turned into a tool error, concurrent handlers
// bounded per session, and an authenticated HTTP mode.
//
// It is OFF unless a host constructs one and calls Run: there is no init(), no
// ambient listener and no default port (REQ-MCP-SERVER-01).
type Server struct {
	*sdk.Server
	opts ServerOptions

	slotsMu sync.Mutex
	slots   map[sdk.Session]*sessionSlots
}

type sessionSlots struct {
	ch    chan struct{}
	users int
}

// MaxConcurrentHandlers bounds the request handlers one session may have
// running at once. Without it a client alone decides how much work this
// process does concurrently.
const MaxConcurrentHandlers = 64

func NewServer(opts ServerOptions) *Server {
	if opts.Info.Name == "" {
		opts.Info = Implementation{Name: "agentkit-go", Version: "0.1.0"}
	}
	s := &Server{opts: opts, slots: map[sdk.Session]*sessionSlots{}}
	s.Server = sdk.NewServer(&s.opts.Info, &sdk.ServerOptions{
		Instructions: opts.Instructions,
		PageSize:     opts.PageSize,
		// tools.listChanged is advertised even before the first tool is
		// registered: a host may register after clients connect.
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}},
		// Private unless a handler says otherwise. A tool list can encode
		// which tools THIS caller may see, and a shared intermediary caching
		// it publicly would serve one tenant's inventory to another.
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) {
			if c.CacheScope == "" {
				c.CacheScope = "private"
			}
		},
	})
	s.AddReceivingMiddleware(s.bound, s.guardToolCalls)
	return s
}

// RegisterTool is REQ-MCP-SERVER-03's explicit registration with a plain
// handler: the arguments arrive decoded (numbers as json.Number, never
// laundered through a float64), and a returned error becomes a tool error
// result. Re-registering a name replaces it. A handler that needs the raw
// request — multi-round-trip input requests, the session — uses the SDK's
// AddTool directly; the cap, audit and panic guard apply to it too.
func (s *Server) RegisterTool(t *Tool, h ToolHandler) error {
	if t == nil || t.Name == "" {
		return errors.New("mcp: a registered tool needs a name")
	}
	if h == nil {
		return fmt.Errorf("mcp: tool %q has no handler", t.Name)
	}
	def := *t
	if def.InputSchema == nil {
		def.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	s.AddTool(&def, func(ctx context.Context, req *sdk.CallToolRequest) (*CallToolResult, error) {
		var args map[string]any
		if raw := req.Params.Arguments; len(raw) > 0 {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&args); err != nil {
				return toolError(fmt.Errorf("arguments: %w", err)), nil
			}
		}
		res, err := h(ctx, args)
		if err != nil {
			return toolError(err), nil
		}
		if res == nil {
			res = &CallToolResult{}
		}
		return res, nil
	})
	return nil
}

func toolError(err error) *CallToolResult {
	return &CallToolResult{IsError: true, Content: []Content{&TextContent{Text: err.Error()}}}
}

// bound is the per-session concurrency cap. A subscriptions/listen stream is
// exempt: it holds its request open for the life of the stream on purpose.
func (s *Server) bound(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if strings.HasPrefix(method, "notifications/") || method == "subscriptions/listen" {
			return next(ctx, method, req)
		}
		sess := req.GetSession()
		s.slotsMu.Lock()
		slot := s.slots[sess]
		if slot == nil {
			slot = &sessionSlots{ch: make(chan struct{}, MaxConcurrentHandlers)}
			s.slots[sess] = slot
		}
		slot.users++
		s.slotsMu.Unlock()
		defer func() {
			s.slotsMu.Lock()
			if slot.users--; slot.users == 0 {
				delete(s.slots, sess)
			}
			s.slotsMu.Unlock()
		}()

		select {
		case slot.ch <- struct{}{}:
			defer func() { <-slot.ch }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return next(ctx, method, req)
	}
}

// guardToolCalls applies AgentKit's policy to every tools/call, however the
// tool was registered: a panicking handler is the host's bug and becomes a
// tool error rather than a dead connection; the result is capped
// (REQ-MCP-CLIENT-09 holds in both directions); and the call is audited. An
// input-required result is not audited — the handler has not finished and
// will be called again.
func (s *Server) guardToolCalls(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (res sdk.Result, err error) {
		p, ok := req.GetParams().(*sdk.CallToolParamsRaw)
		if method != "tools/call" || !ok {
			return next(ctx, method, req)
		}
		defer func() {
			if r := recover(); r != nil {
				res, err = toolError(fmt.Errorf("mcp: handler for %q panicked: %v", p.Name, r)), nil
			}
			cr, _ := res.(*CallToolResult)
			if cr != nil && len(cr.InputRequests) > 0 {
				return
			}
			if cr != nil {
				cr.Content = CapContent(cr.Content)
			}
			if s.opts.Audit != nil {
				s.opts.Audit(core.AuditEvent{Kind: core.AuditToolCall, ToolName: p.Name,
					ArgumentsHash: core.HashArguments(p.Arguments),
					IsError:       err != nil || cr == nil || cr.IsError})
			}
		}()
		return next(ctx, method, req)
	}
}

// ---------------------------------------------------------------- HTTP mode

// HTTPOptions configures the REQ-MCP-SERVER-02 HTTP transport.
type HTTPOptions struct {
	// APIKey is REQUIRED (REQ-MCP-SERVER-07). An empty one is refused rather
	// than defaulting to open: an HTTP listener has none of stdio's process
	// isolation.
	APIKey string
	// Headers names where the key is accepted. Empty means Authorization
	// (Bearer) and X-API-Key.
	Headers []string
	// MaxBodyBytes bounds a request body. Zero, or anything above the
	// server's Limits.MaxMessageBytes, uses that bound instead.
	MaxBodyBytes int64
	// AllowedOrigins is the Origin allowlist. Empty REFUSES every request that
	// carries an Origin header at all: a browser always sends one, a local MCP
	// client never does.
	AllowedOrigins []string
}

// ErrNoAPIKey is REQ-MCP-SERVER-07's refusal.
var ErrNoAPIKey = errors.New("mcp: HTTP mode requires an API key; stdio mode is the " +
	"unauthenticated option and it relies on OS process isolation")

// HTTPHandler serves the protocol over Streamable HTTP with API-key
// authentication.
//
// Authentication runs FIRST, before anything else looks at the request:
// answering 405 tells an unauthenticated caller which verbs exist, and reading
// the body first lets one spend our memory without a credential. Then the
// Origin check (DNS rebinding), then the body is bounded and held to wire's
// rules before the SDK decodes it (REQ-SEC-11).
func (s *Server) HTTPHandler(opts HTTPOptions) (http.Handler, error) {
	if opts.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	headers := opts.Headers
	if len(headers) == 0 {
		headers = []string{"Authorization", "X-API-Key"}
	}
	maxBody := opts.MaxBodyBytes
	if lim := s.opts.Limits.WithDefaults().MaxMessageBytes; maxBody <= 0 || maxBody > lim {
		maxBody = lim
	}
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.Server },
		&sdk.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: maxBody,
			PropagateRequestCancellation: true})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r, opts.APIKey, headers) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !originAllowed(origin, opts.AllowedOrigins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			if err != nil {
				rpcError(w, http.StatusRequestEntityTooLarge, -32600, "request body: "+err.Error())
				return
			}
			if err := wire.Guard(body, s.opts.Limits); err != nil {
				rpcError(w, http.StatusBadRequest, -32700, "malformed message: "+err.Error())
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		h.ServeHTTP(w, r)
	}), nil
}

// rpcError answers with a JSON-RPC error whose id is null: the id could not
// be read, which JSON-RPC 2.0 §5 spells as null.
func rpcError(w http.ResponseWriter, status, code int, msg string) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil,
		"error": map[string]any{"code": code, "message": msg}})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// authorized compares in CONSTANT TIME: an early-returning comparison leaks
// the key one character at a time to anyone who can measure the response.
func authorized(r *http.Request, key string, headers []string) bool {
	for _, h := range headers {
		v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Header.Get(h)), "Bearer "))
		if v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

func originAllowed(origin string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	return false
}
