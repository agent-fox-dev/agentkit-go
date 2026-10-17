package mcpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/wire"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// capture is an slog.Handler that keeps every record it is given.
type capture struct {
	mu    *sync.Mutex
	recs  *[]slog.Record
	attrs []slog.Attr
}

func newCapture() *capture { return &capture{mu: new(sync.Mutex), recs: new([]slog.Record)} }

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }

func (c *capture) Handle(_ context.Context, r slog.Record) error {
	r = r.Clone()
	r.AddAttrs(c.attrs...)
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.recs = append(*c.recs, r)
	return nil
}

func (c *capture) WithAttrs(as []slog.Attr) slog.Handler {
	return &capture{mu: c.mu, recs: c.recs, attrs: append(slices.Clone(c.attrs), as...)}
}

func (c *capture) WithGroup(string) slog.Handler { return c }

func (c *capture) records() []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(*c.recs)
}

// attrsOf is the attributes of r by key.
func attrsOf(r slog.Record) map[string]slog.Value {
	out := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool { out[a.Key] = a.Value; return true })
	return out
}

// callRecord is one call a recorder saw.
type callRecord struct {
	name  string
	input []byte
	res   core.ToolResult
}

// recorder wraps a toolRunner and keeps what each Call received and answered.
type recorder struct {
	toolRunner
	mu    sync.Mutex
	calls []callRecord
}

func (r *recorder) Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult {
	res := r.toolRunner.Call(ctx, name, input)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, callRecord{name, bytes.Clone(input), res})
	return res
}

func (r *recorder) seen() []callRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// rawSession drives a Server with hand-written frames over pipes, so a test
// sees exactly the bytes the server read and the frames it wrote.
type rawSession struct {
	t    *testing.T
	w    io.Writer
	r    *bufio.Scanner
	next int
}

func newRawSession(t *testing.T, s *Server) *rawSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	toClient, fromServer := io.Pipe()
	toServer, fromClient := io.Pipe()
	ss, err := s.open(ctx, agentmcp.NewPipeTransport(toServer, fromServer, wire.Defaults()))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = fromClient.Close()
		_ = ss.close()
		cancel()
	})
	c := &rawSession{t: t, w: fromClient, r: bufio.NewScanner(toClient), next: 1}
	c.r.Buffer(make([]byte, 0, 1<<16), 1<<24)
	c.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`)
	c.read()
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return c
}

func (c *rawSession) send(frame string) {
	c.t.Helper()
	if _, err := io.WriteString(c.w, frame+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

func (c *rawSession) read() map[string]any {
	c.t.Helper()
	if !c.r.Scan() {
		c.t.Fatalf("the server wrote no frame: %v", c.r.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(c.r.Bytes(), &m); err != nil {
		c.t.Fatalf("%s: %v", c.r.Bytes(), err)
	}
	return m
}

// callTool sends a tools/call whose params are the literal params, and returns
// the reply.
func (c *rawSession) callTool(params string) map[string]any {
	c.t.Helper()
	c.next++
	c.send(`{"jsonrpc":"2.0","id":` + strconv.Itoa(c.next) + `,"method":"tools/call","params":` + params + `}`)
	return c.read()
}

// TS-23-13 (integration): tools/call reaches Runner.Call exactly once, with the
// request's arguments byte for byte, or empty when there are none, and mcpserve
// validates nothing itself. 23-REQ-3.1.
func TestCallReachesTheRunnerWithTheRawArguments_TS23_13(t *testing.T) {
	want := core.Tool{Name: "t", Description: "wants a string path", InputSchema: schema.Object(schema.Prop("path", schema.String())),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(map[string]any{"ok": true})
		}}
	rec := &recorder{toolRunner: runnerOf(t, want)}
	s, err := newServer(rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := newRawSession(t, s)

	const odd = `{"path": "a",  "extra":[true]}`
	c.callTool(`{"name":"t","arguments":` + odd + `}`)
	c.callTool(`{"name":"t"}`)
	reply := c.callTool(`{"name":"t","arguments":{"nope": 5}}`)

	calls := rec.seen()
	if len(calls) != 3 {
		t.Fatalf("the runner saw %d calls, want one per request", len(calls))
	}
	if string(calls[0].input) != odd {
		t.Errorf("the first call's input is %q, want the request's own bytes %q", calls[0].input, odd)
	}
	if len(calls[1].input) != 0 {
		t.Errorf("a request with no arguments reached the runner with %q, want empty", calls[1].input)
	}
	if calls[2].res.OK || calls[2].res.Error != "invalid_arguments" {
		t.Errorf("an argument that breaks the schema was not left to the runner: %+v", calls[2].res)
	}
	if res, _ := reply["result"].(map[string]any); res == nil || res["isError"] != true {
		t.Errorf("the schema violation did not come back as an error result: %v", reply)
	}
	for _, call := range calls {
		if call.name != "t" {
			t.Errorf("the runner saw a call for %q", call.name)
		}
	}
}

// fixedRunner answers every call with one result a test can change.
type fixedRunner struct {
	mu  sync.Mutex
	res core.ToolResult
}

func (f *fixedRunner) Tools() []core.Tool {
	return []core.Tool{textTool("t", "fixed", schema.Object())}
}

func (f *fixedRunner) set(res core.ToolResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res = res
}

func (f *fixedRunner) Call(context.Context, string, json.RawMessage) core.ToolResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res
}

// TS-23-14 (property): a result's content is LLMText followed by every
// TextBlock in order, and isError is !OK, for any result. 23-REQ-3.2.
func TestResultContentIsLLMTextAndTextBlocks_TS23_14(t *testing.T) {
	r := &fixedRunner{}
	s, err := newServer(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, s)
	rng := rand.New(rand.NewPCG(23, 14))
	words := []string{"", "ok", "two\nlines", "héllo ✓", `{"quoted": "json"}`, strings.Repeat("x", 300)}
	pick := func() string { return words[rng.IntN(len(words))] }
	for range 200 {
		res := core.ToolResult{OK: rng.IntN(2) == 0, Text: pick(), Detail: pick(), Terminate: rng.IntN(2) == 0}
		if rng.IntN(2) == 0 {
			res.Data = map[string]any{"k": pick(), "n": rng.IntN(100)}
		}
		if !res.OK {
			res.Error = []string{"blocked_by_policy", "panic", "handler_error"}[rng.IntN(3)]
		}
		if rng.IntN(2) == 0 {
			res.Metadata = &core.ToolMetadata{Truncated: true, TotalBytes: 9}
		}
		var texts []string
		for range rng.IntN(5) {
			switch rng.IntN(3) {
			case 0:
				txt := pick()
				res.Blocks = append(res.Blocks, core.TextBlock{Text: txt})
				texts = append(texts, txt)
			case 1:
				res.Blocks = append(res.Blocks, core.ThinkingBlock{Thinking: pick(), Signature: "sig"})
			default:
				res.Blocks = append(res.Blocks, core.ToolUseBlock{ID: "id", Name: "n", Input: json.RawMessage(`{}`)})
			}
		}
		r.set(res)
		out, err := conn.CallRaw(context.Background(), "t", nil)
		if err != nil {
			t.Fatalf("%+v: %v", res, err)
		}
		var got []string
		for _, c := range out.Content {
			tc, ok := c.(*agentmcp.TextContent)
			if !ok {
				t.Fatalf("%+v: content item %T, want text", res, c)
			}
			got = append(got, tc.Text)
		}
		if want := append([]string{res.LLMText()}, texts...); !slices.Equal(got, want) {
			t.Fatalf("%+v: content %q, want %q", res, got, want)
		}
		if out.IsError != !res.OK {
			t.Fatalf("%+v: isError %v", res, out.IsError)
		}
		if out.StructuredContent != nil {
			t.Fatalf("%+v: structuredContent %v is served", res, out.StructuredContent)
		}
	}
}

// failing is a tool that fails in one way.
func failing(name string, run func(context.Context, json.RawMessage) core.ToolResult) core.Tool {
	return core.Tool{Name: name, Description: name, InputSchema: schema.Object(schema.Opt("path", schema.String())), Execute: run}
}

// TS-23-15 (integration): a runner failure arrives as an isError result carrying
// the runner's envelope, never as a protocol error. 23-REQ-3.3.
func TestRunnerFailuresAreResults_TS23_15(t *testing.T) {
	r, err := agentkit.NewRunner(agentkit.Toolset{
		Tools: []core.Tool{
			{Name: "needs_path", Description: "x", InputSchema: schema.Object(schema.Prop("path", schema.String())),
				Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }},
			failing("panics", func(context.Context, json.RawMessage) core.ToolResult { panic("boom") }),
			{Name: "fails", Description: "x", InputSchema: schema.Object(),
				Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("it broke") }},
			failing("aborts", func(context.Context, json.RawMessage) core.ToolResult {
				return core.ErrResult("aborted", "Operation aborted")
			}),
			failing("blocked", func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }),
		},
		Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ToolName == "blocked" {
				return core.BeforeToolCallDecision{Block: true, Reason: "no thanks, not that one"}
			}
			return core.BeforeToolCallDecision{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, s)
	for _, c := range []struct{ tool, input, code, also string }{
		{"needs_path", `{}`, "invalid_arguments", ""},
		{"panics", `{}`, "panic", ""},
		{"fails", `{}`, "handler_error", "it broke"},
		{"aborts", `{}`, "aborted", ""},
		{"blocked", `{}`, "blocked_by_policy", "no thanks, not that one"},
	} {
		out, err := conn.CallRaw(context.Background(), c.tool, json.RawMessage(c.input))
		if err != nil {
			t.Errorf("%s: a protocol error: %v", c.tool, err)
			continue
		}
		var text string
		for _, item := range out.Content {
			if tc, ok := item.(*agentmcp.TextContent); ok {
				text += tc.Text
			}
		}
		if !out.IsError || !strings.Contains(text, c.code) || !strings.Contains(text, c.also) {
			t.Errorf("%s: isError %v, text %q; want an error result naming %q and %q", c.tool, out.IsError, text, c.code, c.also)
		}
	}
}

// blockingTool starts, then waits for its context to end, records the error and
// answers aborted, as the built-in tools do when they are cancelled.
func blockingTool(started chan<- struct{}, observed chan<- error) core.Tool {
	return core.Tool{Name: "block", Description: "blocks", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			started <- struct{}{}
			<-ctx.Done()
			observed <- ctx.Err()
			return core.ErrResult("aborted", "Operation aborted")
		}}
}

// TS-23-16 (integration): cancelling a request, or ending the session, cancels
// the context the runner's call runs with. 23-REQ-3.4.
func TestCancellationReachesTheRunner_TS23_16(t *testing.T) {
	for _, mode := range []string{"the client cancels the request", "the session is closed"} {
		started, observed := make(chan struct{}, 1), make(chan error, 1)
		rec := &recorder{toolRunner: runnerOf(t, blockingTool(started, observed))}
		s, err := newServer(rec, Options{})
		if err != nil {
			t.Fatal(err)
		}
		conn, ss := connectPair(t, s)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = conn.CallRaw(ctx, "block", nil)
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the call never started", mode)
		}
		if mode == "the session is closed" {
			go func() { _ = ss.close() }()
		} else {
			cancel()
		}
		select {
		case err := <-observed:
			if err == nil {
				t.Errorf("%s: the tool saw no context error", mode)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the runner's call was never cancelled", mode)
		}
		cancel()
		<-done
		deadline := time.Now().Add(5 * time.Second)
		for len(rec.seen()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if calls := rec.seen(); len(calls) != 1 || calls[0].res.Error != "aborted" {
			t.Errorf("%s: the runner's result is %+v, want aborted", mode, calls)
		}
	}
}

// TS-23-17 (integration): two calls that each wait for the other both complete,
// so calls are not serialized. 23-REQ-3.5.
func TestCallsRunConcurrently_TS23_17(t *testing.T) {
	var arrived atomic.Int32
	rendezvous := core.Tool{Name: "rendezvous", Description: "waits for a second call", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			arrived.Add(1)
			deadline := time.After(5 * time.Second)
			for arrived.Load() < 2 {
				select {
				case <-deadline:
					return core.ErrResult("timeout", "the other call never arrived")
				case <-ctx.Done():
					return core.ErrResult("aborted", "cancelled")
				case <-time.After(5 * time.Millisecond):
				}
			}
			return core.OKResult(map[string]any{"met": true})
		}}
	s, err := New(runnerOf(t, rendezvous), Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, s)
	var wg sync.WaitGroup
	results := make([]string, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := conn.CallRaw(context.Background(), "rendezvous", nil)
			switch {
			case err != nil:
				results[i] = "error: " + err.Error()
			case out.IsError:
				results[i] = "isError"
			default:
				results[i] = "ok"
			}
		}()
	}
	wg.Wait()
	if results[0] != "ok" || results[1] != "ok" {
		t.Errorf("the calls ended %v, want both ok", results)
	}
}

// TS-23-18 (integration): a call for a tool that is not served is a protocol
// error and never reaches the runner. 23-REQ-3.6.
func TestUnservedToolIsAProtocolError_TS23_18(t *testing.T) {
	rec := &recorder{toolRunner: runnerOf(t, textTool("t", "x", schema.Object()))}
	s, err := newServer(rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = connect(t, s).CallRaw(context.Background(), "nope", json.RawMessage(`{}`))
	var rpc *jsonrpc.Error
	if !errors.As(err, &rpc) {
		t.Errorf("error = %v (%T), want a JSON-RPC protocol error", err, err)
	}
	if n := len(rec.seen()); n != 0 {
		t.Errorf("the runner saw %d calls, want none", n)
	}
}

// TS-23-45 (integration): each served call emits exactly one Info record,
// "tool call", with tool, ok and duration, and the error code on a failure.
// 23-REQ-8.2.
func TestEachCallIsLoggedOnce_TS23_45(t *testing.T) {
	logs := newCapture()
	good := textTool("good", "works", schema.Object())
	bad := core.Tool{Name: "bad", Description: "fails", InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("nope") }}
	s, err := New(runnerOf(t, good, bad), Options{Logger: slog.New(logs)})
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, s)
	for _, name := range []string{"good", "bad"} {
		if _, err := conn.CallRaw(context.Background(), name, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	var calls []slog.Record
	for _, r := range logs.records() {
		if r.Message == "tool call" {
			calls = append(calls, r)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("%d \"tool call\" records, want one per call", len(calls))
	}
	for i, want := range []struct {
		tool string
		ok   bool
		code string
	}{{"good", true, ""}, {"bad", false, "handler_error"}} {
		r, a := calls[i], attrsOf(calls[i])
		if r.Level != slog.LevelInfo {
			t.Errorf("%s: level %v, want Info", want.tool, r.Level)
		}
		if a["tool"].String() != want.tool || a["ok"].Kind() != slog.KindBool || a["ok"].Bool() != want.ok {
			t.Errorf("%s: attributes %v", want.tool, a)
		}
		if d, has := a["duration"]; !has || d.Kind() != slog.KindDuration {
			t.Errorf("%s: no duration attribute: %v", want.tool, a)
		}
		code, has := a["error"]
		if has != (want.code != "") || (has && code.String() != want.code) {
			t.Errorf("%s: error attribute %v (present %v), want %q", want.tool, code, has, want.code)
		}
	}
}
