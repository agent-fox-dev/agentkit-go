package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// collapse folds runs of one event type into one: delta counts are a detail,
// their order against the authoritative events is not.
func collapse(names string) string {
	var out []string
	for _, n := range strings.Split(names, ",") {
		if len(out) == 0 || out[len(out)-1] != n {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

// faux is the executable specification of the event order; the decoder must
// agree with it, block ends after every delta, once per block.
func TestTheEventOrderMatchesFaux(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, streamFixture())
	fs := core.EventStreamOf(faux.New(faux.FauxAssistantMessage(core.StopReasonToolUse,
		faux.FauxText("Hello"), faux.FauxToolCall("toolu_1", "edit_file", toolArgs))).Stream(context.Background(), core.Request{}))
	var fauxEvents []core.Event
	for e := range fs.Events() {
		fauxEvents = append(fauxEvents, e)
	}
	if got, want := collapse(eventNames(x.events)), collapse(faux.EventNames(fauxEvents)); got != want {
		t.Fatalf("event order\n got %s\nwant %s", got, want)
	}
	if ends := strings.Count(eventNames(x.events), "_end,"); ends != 2 {
		t.Fatalf("%d block ends, want one per block: %s", ends, eventNames(x.events))
	}
	if end := x.events[len(x.events)-2].(core.ToolCallEndEvent); string(end.Block.Input) != toolArgs {
		t.Fatalf("tool call end = %+v", end)
	}
}

// The streaming and whole-response paths share one assembler, so a tool call
// keeps the model's own argument bytes on both.
func TestStreamingAndWholeResponsesAgreeByteForByte(t *testing.T) {
	m := testModel()
	streamed := run(t, m, core.Request{}, anthropic.Options{}, 200, streamFixture()).msg
	whole, err := anthropic.DecodeResponse(m, []byte(wholeFixture), nil)
	if err != nil {
		t.Fatal(err)
	}
	var sdkMsg sdk.Message
	if err := json.Unmarshal([]byte(wholeFixture), &sdkMsg); err != nil {
		t.Fatal(err)
	}
	translated, err := anthropic.TranslateMessage(m, sdkMsg)
	if err != nil {
		t.Fatal(err)
	}
	for name, msg := range map[string]*core.AssistantMessage{"streamed": streamed, "whole": whole, "translated": &translated} {
		calls := core.ExtractToolUse(msg)
		if len(calls) != 1 || string(calls[0].Input) != toolArgs || msg.Content.Text() != "Hello" ||
			msg.StopReason != core.StopReasonToolUse || msg.RawStopReason != "tool_use" || msg.ResponseID != "msg_1" {
			t.Errorf("%s: %+v", name, msg)
		}
		if msg.Usage.InputTokens != 100 || msg.Usage.OutputTokens != 42 || msg.Usage.Requests != 1 {
			t.Errorf("%s usage = %+v", name, msg.Usage)
		}
	}
}

// Anthropic's input_tokens already exclude cache reads and writes, so usage
// is reported as sent, a field in message_delta supersedes message_start's,
// and the cost is priced once at the rates of the model that served.
func TestUsageIsReportedAsSentAndPricedAtTheServingModel(t *testing.T) {
	cheap := &core.Model{ID: "claude-cheap", InputCostPerMillion: 0.25, OutputCostPerMillion: 1.25}
	lookup := func(id string) *core.Model {
		if id == "claude-cheap" {
			return cheap
		}
		return nil
	}
	for _, c := range []struct {
		name, served string
		billed       string
		cost         float64
	}{
		{"the requested model", "claude-test", "", (3*100 + 15*42 + 0.3*900 + 3.75*50) / 1e6},
		{"a fallback", "claude-cheap", "claude-cheap", (0.25*100 + 1.25*42) / 1e6},
		{"an unknown fallback", "claude-new", "claude-new", (3*100 + 15*42 + 0.3*900 + 3.75*50) / 1e6},
	} {
		body := strings.Replace(streamFixture(), `"model":"claude-test"`, `"model":"`+c.served+`"`, 1)
		u := run(t, testModel(), core.Request{}, anthropic.Options{BillingLookup: lookup}, 200, body).msg.Usage
		if u.InputTokens != 100 || u.OutputTokens != 42 || u.CacheReadTokens != 900 || u.CacheWriteTokens != 50 {
			t.Errorf("%s: usage = %+v", c.name, u)
		}
		if u.BilledModel != c.billed || !u.Has(core.UsageCostUSD) || math.Abs(u.CostUSD-c.cost) > 1e-12 {
			t.Errorf("%s: billed %q at %.10f, want %q at %.10f", c.name, u.BilledModel, u.CostUSD, c.billed, c.cost)
		}
	}
}

func TestCacheWritesAreReadFromTheTotalOrItsBreakdown(t *testing.T) {
	for _, c := range []struct {
		usage         string
		writes, hour  int64
		reportsAnyway bool
	}{
		{`{"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}`, 50, 20, true},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":20}}`, 50, 20, true},
		{`{"cache_creation":{"ephemeral_5m_input_tokens":30}}`, 30, 0, true},
		{`{}`, 0, 0, false},
	} {
		msg, err := anthropic.DecodeResponse(testModel(), []byte(`{"id":"m","model":"claude-test","content":[],"usage":`+c.usage+`}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		u := msg.Usage
		if u.CacheWriteTokens != c.writes || u.CacheWrite1hTokens != c.hour || u.Has(core.UsageCostUSD) != c.reportsAnyway {
			t.Errorf("usage %s: writes %d, 1h %d, cost set %v", c.usage, u.CacheWriteTokens, u.CacheWrite1hTokens, u.Has(core.UsageCostUSD))
		}
	}

	var start sdk.Usage
	var delta sdk.MessageDeltaUsage
	_ = json.Unmarshal([]byte(`{"input_tokens":150,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}`), &start)
	_ = json.Unmarshal([]byte(`{"output_tokens":42}`), &delta)
	if u := anthropic.TranslateUsage(start, delta); u.InputTokens != 150 || u.OutputTokens != 42 ||
		u.CacheReadTokens != 100 || u.CacheWriteTokens != 50 || u.Requests != 1 {
		t.Errorf("TranslateUsage = %+v", u)
	}
}

// Thinking arrives with its signature, which replay needs, and a redacted
// block carries its opaque data; an empty thinking block is still signed.
func TestThinkingBlocksDecodeWithTheirSignatures(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, sse(
		"message_start", messageStart("claude-test"),
		"content_block_start", `{"index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"step one"}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
		"content_block_stop", `{"index":0}`,
		"content_block_start", `{"index":1,"content_block":{"type":"thinking","thinking":""}}`,
		"content_block_delta", `{"index":1,"delta":{"type":"signature_delta","signature":"SIG2"}}`,
		"content_block_stop", `{"index":1}`,
		"content_block_start", `{"index":2,"content_block":{"type":"redacted_thinking","data":"OPAQUE"}}`,
		"content_block_stop", `{"index":2}`,
		"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
		"message_stop", `{}`,
	))
	want := core.Content{core.ThinkingBlock{Thinking: "step one", Signature: "SIG"}, core.ThinkingBlock{Signature: "SIG2"},
		core.ThinkingBlock{Redacted: true, Signature: "OPAQUE"}}
	if got := x.msg.Content; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("content = %#v", got)
	}
	if !strings.HasPrefix(eventNames(x.events), "message_start,thinking_start,thinking_delta,message_update,thinking_start") {
		t.Errorf("events = %s", eventNames(x.events))
	}
}

// Pings, an event whose data names no type, and events for blocks the
// decoder never saw or cannot model are skipped, and the turn goes on.
func TestTheDecoderSkipsWhatItCannotPlace(t *testing.T) {
	body := sse(
		"ping", `{}`,
		"message_start", messageStart("claude-test"),
		"content_block_delta", `{"index":7,"delta":{"type":"text_delta","text":"orphan"}}`,
		"content_block_stop", `{"index":7}`,
		"content_block_start", `{"index":0,"content_block":{"type":"server_tool_use","id":"s1","name":"web_search"}}`,
		"content_block_stop", `{"index":0}`,
		"content_block_start", `{"index":1,"content_block":{"type":"text","text":"Hi"}}`,
		"content_block_stop", `{"index":1}`,
		"message_delta", `{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
	) + "event: content_block_delta\ndata: {\"index\":1}\n\n" + sse("message_stop", `{}`)
	x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, body)
	if x.err != nil || len(x.msg.Content) != 1 || x.msg.Content.Text() != "Hi" || x.msg.StopReason != core.StopReasonStop {
		t.Fatalf("result = %+v, %v; want the one text block", x.msg, x.err)
	}
	if got := eventNames(x.events); got != "message_start,text_start,message_update,text_end,message_end" {
		t.Errorf("events = %s", got)
	}
}

// A call cut off by max_tokens is representable, its truncated member dropped
// rather than closed, and only the stop reason says it is unsafe to run.
func TestATruncatedToolCallIsSalvagedAndFlaggedByTheStopReason(t *testing.T) {
	for args, want := range map[string]string{
		`{\"path\":\"a.go\",\"new_string\":\"func mai`: `{"path":"a.go"}`,
		`[1,2]`: `{}`,
	} {
		x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, sse(
			"message_start", messageStart("claude-test"),
			"content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"toolu_x","name":"edit_file","input":{}}}`,
			"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"`+args+`"}}`,
			"content_block_stop", `{"index":0}`,
			"message_delta", `{"delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4096}}`,
			"message_stop", `{}`,
		))
		calls := core.ExtractToolUse(x.msg)
		if len(calls) != 1 || string(calls[0].Input) != want || x.msg.StopReason != core.StopReasonLength {
			t.Errorf("%s: calls = %+v, stop %q; want input %s and max_tokens", args, calls, x.msg.StopReason, want)
		}
	}
}

func TestStopDetailsAndSequencesReachTheMessage(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, sse(
		"message_start", messageStart("claude-test"),
		"message_delta", `{"delta":{"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"request declined"}},"usage":{"output_tokens":0}}`,
		"message_stop", `{}`,
	))
	if x.msg.StopReason != core.StopReasonRefusal || x.msg.StopDetail != "cyber: request declined" {
		t.Errorf("streamed: StopReason=%q StopDetail=%q", x.msg.StopReason, x.msg.StopDetail)
	}
	for details, want := range map[string]string{
		`{"category":"cyber"}`: "cyber", `{"explanation":"declined"}`: "declined", `null`: "",
	} {
		msg, err := anthropic.DecodeResponse(testModel(),
			[]byte(`{"id":"m","content":[],"stop_reason":"refusal","stop_details":`+details+`}`), nil)
		if err != nil || msg.StopDetail != want || msg.StopReason != core.StopReasonRefusal {
			t.Errorf("stop_details %s: %+v, %v; want %q", details, msg, err, want)
		}
	}
}

func TestDecodeResponseRefusesWhatItCannotTrust(t *testing.T) {
	for _, body := range []string{
		`{"id":"m","id":"n"}`, `not json`, `{"content":[42]}`,
	} {
		if _, err := anthropic.DecodeResponse(testModel(), []byte(body), nil); err == nil ||
			!strings.HasPrefix(err.Error(), "anthropic: decoding response") {
			t.Errorf("DecodeResponse(%s) err = %v", body, err)
		}
	}
}

// The message records the model that was asked, the one that answered, the
// effort and the provider's clock, and the repair report goes to Warnf.
func TestTheMessageCarriesItsProvenance(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var warned []string
	body := strings.Replace(streamFixture(), `"model":"claude-test"`, `"model":"claude-test-20260101"`, 1)
	x := run(t, testModel(), core.Request{Effort: core.EffortLow, Messages: core.Messages{
		user("go"), core.AssistantMessage{StopReason: core.StopReasonAborted, Content: core.Content{core.TextBlock{Text: "cut"}}},
		user("again")}},
		anthropic.Options{Now: func() time.Time { return at },
			Warnf: func(f string, a ...any) { warned = append(warned, fmt.Sprintf(f, a...)) }},
		200, body)
	m := x.msg
	if m.Model != "claude-test" || m.ResponseModel != "claude-test-20260101" || m.Effort != core.EffortLow || !m.Timestamp.Equal(at) {
		t.Errorf("message = %+v", m)
	}
	if len(warned) != 1 || warned[0] != "anthropic: 1 failed turns dropped" {
		t.Errorf("warnings = %q", warned)
	}
}

type closeFunc struct {
	io.Reader
	close func()
}

func (c closeFunc) Close() error { c.close(); return nil }

// The provider reads its response to the end without waiting for a consumer
// that has not read a single event, and the events are all there afterwards.
func TestStreamingDoesNotWaitForTheConsumer(t *testing.T) {
	closed := make(chan struct{})
	p := anthropic.Provider(*testModel(), anthropic.Options{Env: map[string]string{anthropic.APIKeyVar: testKey},
		Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{},
				Body: closeFunc{strings.NewReader(streamFixture()), func() { close(closed) }}}, nil
		})})
	ch, err := p.Stream(context.Background(), core.Request{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the provider stopped at an unread event")
	}
	var names []string
	for ev := range ch {
		names = append(names, string(ev.Event.EventType()))
	}
	if len(names) < 10 || names[len(names)-1] != string(core.EvMessageEnd) {
		t.Fatalf("events = %v", names)
	}
}
