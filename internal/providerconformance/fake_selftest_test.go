package providerconformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// recorder is a stand-in for *testing.T that keeps what was reported, so a test
// of the harness can observe a failure without failing itself.
type recorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func (r *recorder) failed() bool { return len(r.all()) > 0 }

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.msgs)
}

// post sends body to url with the headers and returns the status and the whole
// response body.
func post(t testing.TB, url string, header map[string]string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func oneText(s string) logicalTurn { return logicalTurn{blocks: []block{textBlock(s)}} }

// payloads decodes the JSON objects a streamed response carries, in order: the
// data lines of an SSE body, or the lines of an NDJSON one. [DONE] is skipped.
func payloads(t testing.TB, raw []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" || !strings.HasPrefix(line, "{") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a streamed line is not JSON: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// dig follows a path of object keys and array indexes through decoded JSON and
// returns nil where it ends.
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

// responseText joins the text deltas of a streamed response.
func responseText(v vendor, raw []byte, t testing.TB) string {
	return textOf(v, payloads(t, raw))
}

// textOf joins the text deltas of the payloads.
func textOf(v vendor, ps []map[string]any) string {
	var b strings.Builder
	for _, p := range ps {
		switch v.name {
		case "anthropic":
			if dig(p, "delta", "type") == "text_delta" {
				b.WriteString(str(dig(p, "delta", "text")))
			}
		case "openrouter":
			b.WriteString(str(dig(p, "choices", 0, "delta", "content")))
		case "ollama":
			b.WriteString(str(dig(p, "message", "content")))
		}
	}
	return b.String()
}

// responseModel is the model id a streamed response names.
func responseModel(v vendor, raw []byte, t testing.TB) string {
	for _, p := range payloads(t, raw) {
		if v.name == "anthropic" {
			if m := str(dig(p, "message", "model")); m != "" {
				return m
			}
		} else if m := str(p["model"]); m != "" {
			return m
		}
	}
	return ""
}

// TS-30-7 (30-REQ-2.1): each fake records method, path, headers and body of
// every request, and answers with the next scripted turn, echoing the model.
func TestFakeRecordsAndAnswersInOrder_TS30_7(t *testing.T) {
	for _, v := range allVendors() {
		t.Run(v.name, func(t *testing.T) {
			rec := &recorder{}
			f := newFake(rec, v, oneText("A"), oneText("B"))
			defer f.Close()
			bodies := [][]byte{[]byte(`{"model":"model-one","n":1}`), []byte(`{"model":"model-two","n":2}`)}
			var raws [][]byte
			for i, body := range bodies {
				status, raw := post(t, f.Endpoint(), map[string]string{"X-Marker": strconv.Itoa(i + 1)}, body)
				if status != http.StatusOK {
					t.Fatalf("request %d: status %d: %s", i+1, status, raw)
				}
				raws = append(raws, raw)
			}
			reqs := f.Requests()
			if len(reqs) != 2 {
				t.Fatalf("recorded %d requests, want 2", len(reqs))
			}
			for i, r := range reqs {
				if r.Method != http.MethodPost || r.Path != v.path || r.Header.Get("X-Marker") != strconv.Itoa(i+1) ||
					!bytes.Equal(r.Body, bodies[i]) {
					t.Errorf("request %d recorded as %s %s marker %q body %q", i+1, r.Method, r.Path, r.Header.Get("X-Marker"), r.Body)
				}
			}
			for i, want := range []struct{ text, model string }{{"A", "model-one"}, {"B", "model-two"}} {
				if got := responseText(v, raws[i], t); got != want.text {
					t.Errorf("response %d carries text %q, want %q", i+1, got, want.text)
				}
				if got := responseModel(v, raws[i], t); got != want.model {
					t.Errorf("response %d names model %q, want the requested %q", i+1, got, want.model)
				}
			}
			if rec.failed() {
				t.Errorf("the fake reported %v for two valid requests", rec.all())
			}
		})
	}
}

// TS-30-8 (30-REQ-2.2): a request to another path, and one beyond the script,
// each fail the subtest and get an HTTP error.
func TestFakeRefusesOtherPathsAndExtraRequests_TS30_8(t *testing.T) {
	for _, v := range allVendors() {
		t.Run(v.name, func(t *testing.T) {
			rec := &recorder{}
			f := newFake(rec, v, oneText("A"))
			defer f.Close()
			other := strings.TrimSuffix(f.Endpoint(), v.path) + "/v1/other"
			if status, _ := post(t, other, nil, []byte(`{"model":"m"}`)); status < 400 {
				t.Errorf("a request to /v1/other got status %d, want an HTTP error", status)
			}
			if msgs := rec.all(); len(msgs) != 1 || !strings.Contains(msgs[0], "/v1/other") {
				t.Fatalf("after the unknown path the fake reported %q, want one failure naming /v1/other", msgs)
			}
			if status, raw := post(t, f.Endpoint(), nil, []byte(`{"model":"m"}`)); status != http.StatusOK {
				t.Fatalf("the first valid request got status %d: %s", status, raw)
			}
			status, _ := post(t, f.Endpoint(), nil, []byte(`{"model":"m"}`))
			if status < 400 {
				t.Errorf("the request beyond the script got status %d, want an HTTP error", status)
			}
			msgs := rec.all()
			if len(msgs) != 2 || !strings.Contains(msgs[1], "request 2") || !strings.Contains(msgs[1], "script of 1") {
				t.Errorf("after the extra request the fake reported %q, want a second failure naming request 2 and a script of 1", msgs)
			}
		})
	}
}

// fullTurn is a turn with every block kind and the usage of the text case.
func fullTurn() logicalTurn {
	return logicalTurn{
		blocks: []block{
			textBlock("Hello there"),
			thinkingBlock("Plan: call echo once.", "sig-conformance"),
			toolBlock("call_conformance_1", "echo", `{"v":"conformance"}`),
		},
		end:   endToolStop,
		usage: &turnUsage{prompt: 100, cacheRead: 60, output: 20, cost: 0.00042},
	}
}

// render answers one request with the turn and returns the status and the raw
// response.
func render(t testing.TB, v vendor, turn logicalTurn) (int, []byte) {
	t.Helper()
	rec := &recorder{}
	f := newFake(rec, v, turn)
	defer f.Close()
	status, raw := post(t, f.Endpoint(), nil, []byte(`{"model":"model-one"}`))
	if rec.failed() {
		t.Fatalf("the fake reported %v", rec.all())
	}
	return status, raw
}

// count is how many payloads satisfy ok.
func count(ps []map[string]any, ok func(map[string]any) bool) int {
	n := 0
	for _, p := range ps {
		if ok(p) {
			n++
		}
	}
	return n
}

// TS-30-9 (30-REQ-2.3): text and thinking arrive as two deltas, tool arguments
// as two fragments (one whole on Ollama), and the usage in each vendor's own
// fields and netting, with a cost only on OpenRouter.
func TestFakeRendersDeltasAndUsage_TS30_9(t *testing.T) {
	const args = `{"v":"conformance"}`
	status, raw := render(t, anthropicVendor, fullTurn())
	if status != http.StatusOK {
		t.Fatalf("anthropic: status %d: %s", status, raw)
	}
	ps := payloads(t, raw)
	delta := func(kind string) func(map[string]any) bool {
		return func(p map[string]any) bool {
			return p["type"] == "content_block_delta" && dig(p, "delta", "type") == kind
		}
	}
	for _, kind := range []string{"text_delta", "thinking_delta", "input_json_delta"} {
		if n := count(ps, delta(kind)); n != 2 {
			t.Errorf("anthropic: %d %s events, want 2", n, kind)
		}
	}
	var fragments, signature string
	for _, p := range ps {
		if delta("input_json_delta")(p) {
			fragments += str(dig(p, "delta", "partial_json"))
		}
		if delta("signature_delta")(p) {
			signature += str(dig(p, "delta", "signature"))
		}
	}
	if fragments != args {
		t.Errorf("anthropic: the argument fragments join to %q, want %q", fragments, args)
	}
	if signature != "sig-conformance" {
		t.Errorf("anthropic: the signature_delta events carry %q, want sig-conformance", signature)
	}
	for _, p := range ps {
		switch p["type"] {
		case "message_start":
			if dig(p, "message", "usage", "input_tokens") != 40.0 || dig(p, "message", "usage", "cache_read_input_tokens") != 60.0 {
				t.Errorf("anthropic: message_start usage %v, want input_tokens 40 (net of the cache) and cache_read_input_tokens 60", dig(p, "message", "usage"))
			}
		case "message_delta":
			if dig(p, "usage", "output_tokens") != 20.0 {
				t.Errorf("anthropic: message_delta usage %v, want output_tokens 20", dig(p, "usage"))
			}
		}
	}
	if bytes.Contains(raw, []byte("cost")) {
		t.Error("anthropic: the response carries a cost")
	}

	status, raw = render(t, openRouterVendor, fullTurn())
	if status != http.StatusOK {
		t.Fatalf("openrouter: status %d: %s", status, raw)
	}
	ps = payloads(t, raw)
	choice := func(path ...any) func(map[string]any) bool {
		return func(p map[string]any) bool {
			v := dig(p, append([]any{"choices", 0, "delta"}, path...)...)
			if s, ok := v.(string); ok {
				return s != ""
			}
			return v != nil
		}
	}
	if n := count(ps, choice("content")); n != 2 {
		t.Errorf("openrouter: %d content deltas, want 2", n)
	}
	if n := count(ps, choice("reasoning_details")); n != 2 {
		t.Errorf("openrouter: %d reasoning_details fragments, want 2", n)
	}
	fragments = ""
	nFragments := 0
	for _, p := range ps {
		if a := str(dig(p, "choices", 0, "delta", "tool_calls", 0, "function", "arguments")); a != "" {
			fragments += a
			nFragments++
		}
	}
	if nFragments != 2 || fragments != args {
		t.Errorf("openrouter: %d argument fragments joining to %q, want 2 joining to %q", nFragments, fragments, args)
	}
	usage := dig(ps[len(ps)-1], "usage")
	if dig(usage, "prompt_tokens") != 100.0 || dig(usage, "prompt_tokens_details", "cached_tokens") != 60.0 ||
		dig(usage, "completion_tokens") != 20.0 || dig(usage, "cost") != 0.00042 {
		t.Errorf("openrouter: usage %v, want prompt_tokens 100, cached_tokens 60, completion_tokens 20 and cost 0.00042", usage)
	}

	status, raw = render(t, ollamaVendor, fullTurn())
	if status != http.StatusOK {
		t.Fatalf("ollama: status %d: %s", status, raw)
	}
	ps = payloads(t, raw)
	if n := count(ps, func(p map[string]any) bool { return str(dig(p, "message", "content")) != "" }); n != 2 {
		t.Errorf("ollama: %d content lines, want 2", n)
	}
	if n := count(ps, func(p map[string]any) bool { return str(dig(p, "message", "thinking")) != "" }); n != 2 {
		t.Errorf("ollama: %d thinking lines, want 2", n)
	}
	var calls []map[string]any
	for _, p := range ps {
		if tc, ok := dig(p, "message", "tool_calls").([]any); ok {
			for _, c := range tc {
				calls = append(calls, c.(map[string]any))
			}
		}
	}
	if len(calls) != 1 {
		t.Fatalf("ollama: %d tool calls, want 1, whole", len(calls))
	}
	if got, _ := json.Marshal(dig(calls[0], "function", "arguments")); string(got) != args {
		t.Errorf("ollama: the tool call arguments are %s, want the whole object %s", got, args)
	}
	done := ps[len(ps)-1]
	if done["done"] != true || done["prompt_eval_count"] != 100.0 || done["prompt_eval_cached_count"] != 60.0 || done["eval_count"] != 20.0 {
		t.Errorf("ollama: final line %v, want prompt_eval_count 100, prompt_eval_cached_count 60 and eval_count 20", done)
	}
	if bytes.Contains(raw, []byte("cost")) {
		t.Error("ollama: the response carries a cost")
	}
}

// TS-30-10 (30-REQ-2.4): each ending is rendered the way the vendor sends it.
func TestFakeRendersEveryEndingAsTheVendorDoes_TS30_10(t *testing.T) {
	text := []block{textBlock("Some text")}
	call := []block{textBlock("Some text"), toolBlock("call_1", "echo", `{"v":"x"}`)}
	// stop is the vendor's own spelling of the stop in a normal or tool-call
	// ending: the field and the value it carries.
	stop := func(v vendor, ps []map[string]any) string {
		for _, p := range ps {
			switch v.name {
			case "anthropic":
				if p["type"] == "message_delta" {
					return str(dig(p, "delta", "stop_reason"))
				}
			case "openrouter":
				if s := str(dig(p, "choices", 0, "finish_reason")); s != "" {
					return s
				}
			case "ollama":
				if p["done"] == true {
					return str(p["done_reason"])
				}
			}
		}
		return ""
	}
	wantNormal := map[string]string{"anthropic": "end_turn", "openrouter": "stop", "ollama": "stop"}
	wantToolStop := map[string]string{"anthropic": "tool_use", "openrouter": "tool_calls", "ollama": "stop"}
	terminal := func(v vendor, raw []byte, ps []map[string]any) bool {
		switch v.name {
		case "anthropic":
			return bytes.Contains(raw, []byte(`"message_stop"`))
		case "openrouter":
			return bytes.Contains(raw, []byte("[DONE]"))
		}
		return count(ps, func(p map[string]any) bool { return p["done"] == true }) > 0
	}
	for _, v := range allVendors() {
		t.Run(v.name, func(t *testing.T) {
			for _, c := range []struct {
				name   string
				blocks []block
				end    ending
				want   string
			}{
				{"normal stop", text, endNormal, wantNormal[v.name]},
				{"tool-call stop", call, endToolStop, wantToolStop[v.name]},
			} {
				status, raw := render(t, v, logicalTurn{blocks: c.blocks, end: c.end})
				ps := payloads(t, raw)
				if status != http.StatusOK || stop(v, ps) != c.want || !terminal(v, raw, ps) {
					t.Errorf("%s: status %d, stop %q, terminal signal %v; want 200, %q and the terminal signal", c.name, status, stop(v, ps), terminal(v, raw, ps), c.want)
				}
			}

			status, raw := render(t, v, logicalTurn{blocks: call, end: endCutOff})
			ps := payloads(t, raw)
			if status != http.StatusOK || stop(v, ps) != "" || terminal(v, raw, ps) {
				t.Errorf("cut off: status %d, stop %q, terminal signal %v; want 200 and neither a stop nor the final signal", status, stop(v, ps), terminal(v, raw, ps))
			}
			if len(ps) == 0 {
				t.Error("cut off: the body carries nothing before the cut")
			}
			// The last block, the tool call, is cut: Anthropic and OpenRouter stop
			// inside its arguments, Ollama sends the call whole and then stops.
			var args strings.Builder
			wholeCalls := 0
			for _, p := range ps {
				args.WriteString(str(dig(p, "delta", "partial_json")))
				args.WriteString(str(dig(p, "choices", 0, "delta", "tool_calls", 0, "function", "arguments")))
				if calls, ok := dig(p, "message", "tool_calls").([]any); ok {
					wholeCalls += len(calls)
				}
			}
			switch {
			case v.name == "ollama" && wholeCalls != 1:
				t.Errorf("cut off: %d whole tool calls before the cut, want 1", wholeCalls)
			case v.name != "ollama" && (args.Len() == 0 || json.Valid([]byte(args.String()))):
				t.Errorf("cut off: the arguments before the cut are %q, want the start of an object that is not complete", args.String())
			}

			status, raw = render(t, v, logicalTurn{end: endStatus, status: 400, message: "the-vendor-message"})
			var errBody map[string]any
			if err := json.Unmarshal(raw, &errBody); err != nil || status != 400 {
				t.Fatalf("http status: status %d, body %q (%v); want 400 and a JSON body", status, raw, err)
			}
			var msg any
			switch v.name {
			case "anthropic":
				msg = dig(errBody, "error", "message")
				if errBody["type"] != "error" {
					t.Errorf("http status: body %v, want type error", errBody)
				}
			case "openrouter":
				msg = dig(errBody, "error", "message")
				if dig(errBody, "error", "code") != 400.0 {
					t.Errorf("http status: body %v, want error.code 400", errBody)
				}
			case "ollama":
				msg = errBody["error"]
			}
			if msg != "the-vendor-message" {
				t.Errorf("http status: body %v, want the vendor's message in its own place", errBody)
			}

			status, raw = render(t, v, logicalTurn{blocks: text, end: endInStream, message: "the-vendor-message"})
			ps = payloads(t, raw)
			errAt := -1
			for i, p := range ps {
				switch v.name {
				case "anthropic":
					if p["type"] == "error" && dig(p, "error", "message") == "the-vendor-message" {
						errAt = i
					}
				case "openrouter":
					if dig(p, "error", "code") == 502.0 && dig(p, "error", "message") == "the-vendor-message" {
						errAt = i
					}
				case "ollama":
					if p["error"] == "the-vendor-message" {
						errAt = i
					}
				}
			}
			if status != http.StatusOK || errAt < 1 || errAt != len(ps)-1 {
				t.Errorf("in-stream error: status %d, error at payload %d of %d; want 200 and the error as the last payload, after the content", status, errAt, len(ps))
			} else if got := textOf(v, ps[:errAt]); got != "Some text" {
				t.Errorf("in-stream error: the text before the error is %q, want the whole block %q", got, "Some text")
			}
			if v.name == "anthropic" && !bytes.Contains(raw, []byte("event: error")) {
				t.Error("in-stream error: the Anthropic body has no `event: error` line")
			}
			if terminal(v, raw, ps) {
				t.Error("in-stream error: the body also carries the final signal")
			}
		})
	}
}

// summaryBodies are hand-written request bodies in each vendor's form, all
// saying the same thing: model m-1, effort medium, the tool echo, the user text
// "say hi", an assistant message of "Calling it." with the thinking "Plan: call
// echo once." and the call call_1 to echo with {"v":"x"}, and its result.
var summaryBodies = map[string]string{
	"anthropic": `{"model":"m-1","max_tokens":1,"stream":true,"output_config":{"effort":"medium"},
		"tools":[{"name":"echo","description":"echo","input_schema":{"type":"object"}}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"say hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"Plan: call echo once.","signature":"sig-conformance"},
				{"type":"text","text":"Calling it."},
				{"type":"tool_use","id":"call_1","name":"echo","input":{"v":"x"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"{\"echoed\":true}"}]}]}]}`,
	"openrouter": `{"model":"m-1","reasoning":{"effort":"medium"},
		"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"say hi"},
			{"role":"assistant","content":"Calling it.",
			 "tool_calls":[{"id":"call_1","type":"function","function":{"name":"echo","arguments":"{\"v\":\"x\"}"}}],
			 "reasoning_details":[
				{"type":"reasoning.text","text":"Plan: call ","format":"unknown","index":0},
				{"type":"reasoning.text","text":"echo once.","format":"unknown","index":0}]},
			{"role":"tool","tool_call_id":"call_1","content":"{\"echoed\":true}"}]}`,
	"ollama": `{"model":"m-1","think":"medium",
		"tools":[{"type":"function","function":{"name":"echo","parameters":{"type":"object"}}}],
		"messages":[
			{"role":"user","content":"say hi"},
			{"role":"assistant","content":"Calling it.","thinking":"Plan: call echo once.",
			 "tool_calls":[{"id":"call_1","function":{"index":0,"name":"echo","arguments":{"v":"x"}}}]},
			{"role":"tool","content":"{\"echoed\":true}","tool_name":"echo","tool_call_id":"call_1"}]}`,
}

// TS-30-11 (30-REQ-2.5): the three decoders give the same summary of the same
// request, with the replayed thinking also in each vendor's raw form.
func TestFakeDecodesRequestsToOneSummary_TS30_11(t *testing.T) {
	var sums []requestSummary
	for _, v := range allVendors() {
		s, err := v.decode([]byte(summaryBodies[v.name]))
		if err != nil {
			t.Fatalf("%s: decode: %v", v.name, err)
		}
		sums = append(sums, s)

		if s.Model != "m-1" || s.Effort != "medium" || !slices.Equal(s.Tools, []string{"echo"}) ||
			!slices.Equal(s.UserTexts, []string{"say hi"}) {
			t.Errorf("%s: model %q, effort %q, tools %v, user texts %v; want m-1, medium, [echo] and [say hi]", v.name, s.Model, s.Effort, s.Tools, s.UserTexts)
		}
		if len(s.Results) != 1 || s.Results[0].CallID != "call_1" || s.Results[0].Text != `{"echoed":true}` {
			t.Errorf("%s: tool results %+v, want one for call_1 with the echoed text", v.name, s.Results)
		}
		if len(s.Assistants) != 1 {
			t.Fatalf("%s: %d assistant messages, want 1", v.name, len(s.Assistants))
		}
		a := s.Assistants[0]
		if a.Text != "Calling it." || a.Thinking != "Plan: call echo once." {
			t.Errorf("%s: assistant text %q and thinking %q, want %q and %q", v.name, a.Text, a.Thinking, "Calling it.", "Plan: call echo once.")
		}
		if len(a.ToolCalls) != 1 || a.ToolCalls[0].ID != "call_1" || a.ToolCalls[0].Name != "echo" || !jsonEqual(a.ToolCalls[0].Args, []byte(`{"v":"x"}`)) {
			t.Errorf("%s: tool calls %+v, want call_1 to echo with {\"v\":\"x\"}", v.name, a.ToolCalls)
		}
		var wantRaw string
		switch v.name {
		case "anthropic":
			wantRaw = `{"thinking":"Plan: call echo once.","signature":"sig-conformance"}`
		case "openrouter":
			wantRaw = `[{"type":"reasoning.text","text":"Plan: call ","format":"unknown","index":0},{"type":"reasoning.text","text":"echo once.","format":"unknown","index":0}]`
		case "ollama":
			wantRaw = `"Plan: call echo once."`
		}
		if !jsonEqual(a.RawThinking, []byte(wantRaw)) {
			t.Errorf("%s: raw thinking %s, want %s", v.name, a.RawThinking, wantRaw)
		}
	}
	// What is not vendor form is equal across the three.
	for i := 1; i < len(sums); i++ {
		a, b := sums[0], sums[i]
		if a.Model != b.Model || a.Effort != b.Effort || !slices.Equal(a.Tools, b.Tools) || !slices.Equal(a.UserTexts, b.UserTexts) ||
			!slices.Equal(a.Results, b.Results) {
			t.Errorf("the summary of %d differs from the first: %+v and %+v", i, b, a)
		}
	}
}

// A decoder that reads the wrong field would agree with a renderer that writes
// the same wrong field, so each must be able to say a body is not its form.
func TestFakeDecodeRejectsAMalformedBody_TS30_11(t *testing.T) {
	for _, v := range allVendors() {
		if _, err := v.decode([]byte(`{"model":`)); err == nil {
			t.Errorf("%s: decoding truncated JSON succeeded", v.name)
		}
	}
}

// TS-30-12 (30-REQ-2.6): the fake sources import the standard library alone.
func TestFakeFilesImportOnlyTheStandardLibrary_TS30_12(t *testing.T) {
	files := []string{"fake_test.go", "fake_anthropic_test.go", "fake_openrouter_test.go", "fake_ollama_test.go"}
	for _, file := range files {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %s, which is not in the standard library", file, path)
			}
			for _, banned := range []string{"anthropic-sdk-go", "OpenRouterTeam", "ollama/ollama", "agentkit-go"} {
				if strings.Contains(path, banned) {
					t.Errorf("%s imports %s, a vendor SDK or a package of the code under test", file, path)
				}
			}
		}
	}
}

// A turn scripted without usage still reports one, as a real vendor always does,
// in each vendor's own field.
func TestFakeReportsAUsageWhenNoneIsScripted_TS30_9(t *testing.T) {
	for _, c := range []struct {
		v    vendor
		path []any
		pick func([]map[string]any) map[string]any
	}{
		{anthropicVendor, []any{"message", "usage", "input_tokens"}, func(ps []map[string]any) map[string]any { return ps[0] }},
		{openRouterVendor, []any{"usage", "prompt_tokens"}, func(ps []map[string]any) map[string]any { return ps[len(ps)-1] }},
		{ollamaVendor, []any{"prompt_eval_count"}, func(ps []map[string]any) map[string]any { return ps[len(ps)-1] }},
	} {
		status, raw := render(t, c.v, oneText("x"))
		if status != http.StatusOK {
			t.Fatalf("%s: status %d: %s", c.v.name, status, raw)
		}
		if n, _ := dig(c.pick(payloads(t, raw)), c.path...).(float64); n <= 0 {
			t.Errorf("%s: a turn with no scripted usage reports %v at %v, want a positive count", c.v.name, n, c.path)
		}
	}
}

// jsonEqual is the oracle the cases compare arguments and thinking with, so it
// must call different values different, and the same value in another order or
// spacing the same.
func TestJSONEqual_TS30_11(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, `{"a":1,"b":null}`, false},
		{`[1,2]`, `[2,1]`, false},
		{`"x"`, `"x"`, true},
		{`{"a":`, `{"a":1}`, false},
		{`{"a":`, `{"a":`, false},
		{`{"a":1}`, ``, false},
	} {
		if got := jsonEqual([]byte(c.a), []byte(c.b)); got != c.want {
			t.Errorf("jsonEqual(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// OpenRouter reports a cost only when the turn scripts one: a turn without it is
// priced by whoever reads it.
func TestFakeReportsNoCostUnlessScripted_TS30_9(t *testing.T) {
	_, raw := render(t, openRouterVendor, logicalTurn{blocks: []block{textBlock("x")}, usage: &turnUsage{prompt: 10, output: 5}})
	if bytes.Contains(raw, []byte("cost")) {
		t.Errorf("openrouter: a turn with no scripted cost reports one:\n%s", raw)
	}
}

// Env is what the case runner hands the provider as its only variables: the
// provider's own, each pointing at this fake's endpoint.
func TestFakeEnvIsTheProvidersAndPointsAtTheFake_TS30_3(t *testing.T) {
	want := map[string]struct {
		keys   []string
		urlKey string
		suffix string // what the provider appends to the URL to reach the endpoint
	}{
		"anthropic":  {[]string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}, "ANTHROPIC_BASE_URL", "/v1/messages"},
		"openrouter": {[]string{"OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"}, "OPENROUTER_BASE_URL", "/chat/completions"},
		"ollama":     {[]string{"OLLAMA_HOST"}, "OLLAMA_HOST", "/api/chat"},
	}
	for _, v := range allVendors() {
		f := newFake(&recorder{}, v)
		env, w := f.Env(), want[v.name]
		var keys []string
		for k, val := range env {
			keys = append(keys, k)
			if val == "" {
				t.Errorf("%s: %s is empty", v.name, k)
			}
		}
		slices.Sort(keys)
		if !slices.Equal(keys, w.keys) {
			t.Errorf("%s: the variables are %v, want exactly %v", v.name, keys, w.keys)
		}
		if got := env[w.urlKey] + w.suffix; got != f.Endpoint() {
			t.Errorf("%s: %s=%s leads to %s, want the fake's endpoint %s", v.name, w.urlKey, env[w.urlKey], got, f.Endpoint())
		}
		if v.name == "openrouter" && !strings.HasSuffix(env[w.urlKey], "/api/v1") {
			t.Errorf("openrouter: the base URL %s does not end in /api/v1", env[w.urlKey])
		}
		f.Close()
	}
}

// Two tool calls in one turn are two calls on every wire: numbered apart where
// the wire numbers them, so a reader cannot merge them.
func TestFakeNumbersToolCallsApart_TS30_9(t *testing.T) {
	turn := logicalTurn{
		blocks: []block{toolBlock("call_a", "echo", `{"v":"a"}`), toolBlock("call_b", "echo", `{"v":"b"}`)},
		end:    endToolStop,
	}
	for _, c := range []struct {
		v    vendor
		path func(p map[string]any) any
	}{
		{openRouterVendor, func(p map[string]any) any {
			if str(dig(p, "choices", 0, "delta", "tool_calls", 0, "id")) == "" {
				return nil
			}
			return dig(p, "choices", 0, "delta", "tool_calls", 0, "index")
		}},
		{ollamaVendor, func(p map[string]any) any { return dig(p, "message", "tool_calls", 0, "function", "index") }},
	} {
		_, raw := render(t, c.v, turn)
		var idx []float64
		for _, p := range payloads(t, raw) {
			if n, ok := c.path(p).(float64); ok {
				idx = append(idx, n)
			}
		}
		if !slices.Equal(idx, []float64{0, 1}) {
			t.Errorf("%s: the two calls are numbered %v, want [0 1]", c.v.name, idx)
		}
	}
	_, raw := render(t, anthropicVendor, turn)
	var starts []float64
	for _, p := range payloads(t, raw) {
		if p["type"] == "content_block_start" {
			starts = append(starts, p["index"].(float64))
		}
	}
	if !slices.Equal(starts, []float64{0, 1}) {
		t.Errorf("anthropic: the two blocks are numbered %v, want [0 1]", starts)
	}
}
