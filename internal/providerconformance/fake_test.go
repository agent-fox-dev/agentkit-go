package providerconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
)

// The vendor fakes render logical turns in three vendors' native wire formats
// and decode the requests they get back. They use the standard library and
// encoding/json alone: a fake built on a vendor SDK, or on a provider's own
// test helpers, would agree with any bug in the code under test.

// reporter is the part of testing.TB the fakes and the case runner report
// through. *testing.T satisfies it; a test of the harness itself passes a
// recorder, so a failure can be observed without failing that test.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

type blockKind int

const (
	blockText blockKind = iota
	blockThinking
	blockToolCall
)

// block is one block of a logical turn.
type block struct {
	kind blockKind
	// text is the text of a text block, and the thinking of a thinking one.
	text string
	// signature is the signature of a thinking block. Only Anthropic sends one.
	signature string
	// id, name and args are a tool call: its id, the tool, and the arguments as
	// a JSON object.
	id, name, args string
}

func textBlock(text string) block { return block{kind: blockText, text: text} }

func thinkingBlock(thinking, signature string) block {
	return block{kind: blockThinking, text: thinking, signature: signature}
}

func toolBlock(id, name, args string) block {
	return block{kind: blockToolCall, id: id, name: name, args: args}
}

// ending is how a logical turn ends.
type ending int

const (
	// endNormal is the vendor's normal stop.
	endNormal ending = iota
	// endToolStop is the vendor's tool-call stop (Anthropic tool_use, OpenRouter
	// tool_calls, Ollama stop).
	endToolStop
	// endCutOff is a body that stops after the blocks without the vendor's final
	// signal, the last block cut after its first delta.
	endCutOff
	// endStatus is an HTTP error status with the vendor's error body, before any
	// content.
	endStatus
	// endInStream is the vendor's in-stream error after the blocks.
	endInStream
)

// turnUsage is a turn's usage as one logical set of numbers: the prompt tokens
// including the cache reads, the cache reads, the output tokens, and the cost a
// vendor that reports one reports.
type turnUsage struct {
	prompt, cacheRead, output int
	cost                      float64
}

// logicalTurn is a vendor-neutral script of one model response.
type logicalTurn struct {
	blocks []block
	end    ending
	// status and message are the HTTP status of an endStatus turn, and the
	// message of that and of an endInStream turn.
	status  int
	message string
	// usage is nil for a turn whose usage does not matter; the fakes then report
	// a small one, as a real vendor always does.
	usage *turnUsage
}

// usageOrDefault is the usage the fakes report for t.
func (t logicalTurn) usageOrDefault() turnUsage {
	if t.usage != nil {
		return *t.usage
	}
	return turnUsage{prompt: 10, output: 5}
}

// toolCall is a tool call of a request summary.
type toolCall struct {
	ID, Name string
	Args     json.RawMessage
}

// assistantSummary is one assistant message of a request.
type assistantSummary struct {
	// Text is the visible text.
	Text      string
	ToolCalls []toolCall
	// Thinking is the replayed thinking as text, and RawThinking the same in the
	// vendor's own form: Anthropic {"thinking","signature"}, OpenRouter the
	// reasoning_details array, Ollama the JSON string. Both are empty when no
	// thinking was replayed.
	Thinking    string
	RawThinking json.RawMessage
}

// toolResult is a tool result of a request: the call it answers and its text.
type toolResult struct{ CallID, Text string }

// requestSummary is the vendor-neutral decoding of a request body.
type requestSummary struct {
	Model string
	// Effort is the effort as the vendor's field carries it: Anthropic
	// output_config.effort, OpenRouter reasoning.effort, Ollama think.
	Effort     string
	Tools      []string
	UserTexts  []string
	Assistants []assistantSummary
	Results    []toolResult
}

// recordedRequest is a request a fake received.
type recordedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// vendor is what differs between the three fakes.
type vendor struct {
	name string
	// path is the endpoint the fake serves, POST only.
	path string
	// env is the provider's variables for a fake served at url, and only those.
	env func(url string) map[string]string
	// render answers one request with the turn, in the vendor's wire format,
	// echoing the model the request named.
	render func(w http.ResponseWriter, model string, t logicalTurn)
	// decode summarizes a request body.
	decode func(body []byte) (requestSummary, error)
}

// allVendors lists the three vendors in the catalog's order.
func allVendors() []vendor { return []vendor{anthropicVendor, openRouterVendor, ollamaVendor} }

// fake is an httptest server that replays scripted logical turns, one per
// request, and records every request.
type fake struct {
	v   vendor
	rep reporter
	srv *httptest.Server
	// release ends a request that Hang is holding.
	release chan struct{}
	once    sync.Once

	mu     sync.Mutex
	script []logicalTurn
	reqs   []recordedRequest
	served int
	hang   bool
}

// newFake starts a fake of v that answers its requests with turns in order. A
// request to another path, or beyond the script, is reported to rep as an error
// and answered with an HTTP error status. The caller closes it.
func newFake(rep reporter, v vendor, turns ...logicalTurn) *fake {
	f := &fake{v: v, rep: rep, script: turns, release: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Close stops the server, releasing any request held by Hang.
func (f *fake) Close() {
	f.once.Do(func() { close(f.release) })
	f.srv.Close()
}

// URL is the address of the server.
func (f *fake) URL() string { return f.srv.URL }

// Endpoint is the URL requests are sent to.
func (f *fake) Endpoint() string { return f.srv.URL + f.v.path }

// Env is the provider's variables, pointed at this fake.
func (f *fake) Env() map[string]string { return f.v.env(f.srv.URL) }

// Requests returns what the fake has received, in order.
func (f *fake) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// Hang makes the fake hold every request it gets from now on without answering,
// until the client gives up or the fake is closed.
func (f *fake) Hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = true
}

// Summaries decodes the requests the fake received at its endpoint.
func (f *fake) Summaries() ([]requestSummary, error) {
	var out []requestSummary
	for i, r := range f.Requests() {
		if r.Path != f.v.path {
			continue
		}
		s, err := f.v.decode(r.Body)
		if err != nil {
			return nil, fmt.Errorf("%s fake: request %d: %w", f.v.name, i+1, err)
		}
		out = append(out, s)
	}
	return out, nil
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	rec := recordedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}

	f.mu.Lock()
	f.reqs = append(f.reqs, rec)
	var turn logicalTurn
	var problem string
	var status int
	switch {
	case r.Method != http.MethodPost || r.URL.Path != f.v.path:
		problem = fmt.Sprintf("%s fake: unexpected request %s %s; it serves POST %s only", f.v.name, r.Method, r.URL.Path, f.v.path)
		status = http.StatusNotFound
	case f.served >= len(f.script):
		problem = fmt.Sprintf("%s fake: request %d is beyond a script of %d", f.v.name, f.served+1, len(f.script))
		status = http.StatusGone
	default:
		turn = f.script[f.served]
		f.served++
	}
	hang := f.hang
	f.mu.Unlock()

	if problem != "" {
		f.rep.Errorf("%s", problem)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": problem})
		return
	}
	if hang {
		select {
		case <-r.Context().Done():
		case <-f.release:
		}
		return
	}
	f.v.render(w, modelOf(body), turn)
}

// modelOf is the "model" a request body names.
func modelOf(body []byte) string {
	var b struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &b)
	return b.Model
}

// flush sends what has been written so far, so the client reads it as a chunk.
func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSE writes one server-sent event, with an event line when event is set.
func writeSSE(w http.ResponseWriter, event string, data any) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(data))
	flush(w)
}

// writeLine writes one NDJSON line.
func writeLine(w http.ResponseWriter, data any) {
	fmt.Fprintf(w, "%s\n", mustJSON(data))
	flush(w)
}

// writeJSONStatus answers with a JSON body and a status.
func writeJSONStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, "%s\n", mustJSON(data))
}

// mustJSON encodes v. The values are built by this file, so it cannot fail.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fake: %v", err))
	}
	return string(b)
}

// halves splits s into at most two non-empty parts, at the middle rune. The
// fakes send text, thinking and tool arguments in these two deltas.
func halves(s string) []string {
	r := []rune(s)
	switch len(r) {
	case 0:
		return nil
	case 1:
		return []string{s}
	}
	return []string{string(r[:len(r)/2]), string(r[len(r)/2:])}
}

// jsonEqual reports whether a and b are the same JSON value.
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(bytes.TrimSpace(a), &x) != nil || json.Unmarshal(bytes.TrimSpace(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// rawJSON is v as JSON bytes, for the raw-thinking fields of a summary.
func rawJSON(v any) json.RawMessage { return json.RawMessage(mustJSON(v)) }

// wireContent reads a message's "content" that is either a string or an array
// of blocks with a "text" field, as the vendors accept, and joins the text.
func wireContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out string
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			out += p.Text
		}
	}
	return out
}
