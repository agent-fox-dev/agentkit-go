package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// NFR-TEST-08(b) and 10-REQ-8: the Anthropic request body, byte for byte.
//
// One canonical request exercises everything the wire encodes: two system
// blocks, a prefix, history with an image, a replayed tool call whose input
// has the model's own spacing, its result, two tools (one carrying the
// fields that must never reach the wire), and an effort on an adaptive
// model. The body captured is the real one — the bytes the transport saw —
// and the golden holds those bytes exactly, unindented: indenting would
// rewrite the spacing inside the replayed tool input, which is the very
// thing the golden pins.

type goldenCase struct {
	name string
	body string
}

// canonicalModelID is the catalog row the golden request is built for. It
// takes adaptive thinking.
const canonicalModelID = "claude-opus-5-5"

// canonicalToolInput is the replayed tool_use input, spaced as a model might
// write it. It must reach the wire exactly so (10-REQ-6.1).
const canonicalToolInput = `{"pattern": "**/*.go",  "limit": 5}`

func canonicalRequest(t *testing.T) core.Request {
	t.Helper()
	m, _ := catalog.Lookup(canonicalModelID)
	maxTok := 1024
	stamp := func(msg core.AssistantMessage) core.AssistantMessage {
		msg.Model = m.ID
		return msg
	}
	return core.Request{
		System: []core.ContentBlock{
			core.TextBlock{Text: "You are a release engineer."},
			core.TextBlock{Text: "Answer from the repository, not from memory."},
		},
		Prefix: core.Messages{
			core.UserMessage{Content: core.Content{core.TextBlock{Text: "Project: agentkit-go. Branch: main."}}},
			stamp(core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "Understood."}},
				StopReason: core.StopReasonStop}),
		},
		Messages: core.Messages{
			core.UserMessage{Content: core.Content{
				core.TextBlock{Text: "Which Go files changed?"},
			}},
			stamp(core.AssistantMessage{
				Content: core.Content{core.TextBlock{Text: "Let me look."},
					core.ToolUseBlock{ID: "call_1", Name: "find_files", Input: json.RawMessage(canonicalToolInput)}},
				StopReason: core.StopReasonToolUse,
			}),
			core.ToolResultMessage{
				ToolUseID: "call_1", ToolName: "find_files",
				Content: core.Content{core.TextBlock{
					Text: `{"ok":true,"data":{"entries":["main.go"]}}`}},
				Metadata: func() *core.ToolMetadata {
					two := 2
					return &core.ToolMetadata{
						Truncated:   true,
						TruncatedBy: "bytes",
						TotalBytes:  123,
						SpillPath:   "/tmp/agentkit-golden-sentinel.log",
						DurationMS:  42,
						ExitCode:    &two,
						Outcome:     "exit",
						LineEnding:  "lf",
					}
				}(),
			},
		},
		Tools:      core.ToolWires(canonicalTools(nil)),
		ToolChoice: core.ToolChoiceAuto,
		MaxTokens:  &maxTok,
		Effort:     core.EffortHigh,
	}
}

// canonicalTools is the golden request's tools: find_files, with out as its
// output schema, and a terminating submit tool that reaches a child.
func canonicalTools(out *schema.Schema) []core.Tool {
	noop := func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }
	return []core.Tool{
		{
			Name:        "find_files",
			Description: "Find files by glob pattern.",
			InputSchema: schema.Object(
				schema.Prop("pattern", schema.String("Glob pattern")),
				schema.Opt("limit", schema.Int("Maximum results")),
			),
			OutputSchema: out,
			Execute:      noop,
		},
		{
			Name:           "submit_answer",
			Description:    "Submit the final answer.",
			InputSchema:    schema.Object(schema.Prop("answer", schema.String("The answer"))),
			OutputSchema:   schema.Object(schema.Prop("accepted", schema.Bool())),
			ReachableTools: []core.Tool{{Name: "reachable_child", InputSchema: schema.Object(), Execute: noop}},
			Terminating:    true,
			Execute:        noop,
		},
	}
}

// canonicalModel is the catalog row for canonicalModelID.
func canonicalModel(t *testing.T) *core.Model {
	t.Helper()
	m, ok := catalog.Lookup(canonicalModelID)
	if !ok {
		t.Fatalf("%s is not in the catalog", canonicalModelID)
	}
	return &m
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// capture drives the Anthropic provider built from opts and returns the
// request body it sent.
func capture(t *testing.T, opts anthropic.Options, m *core.Model, req core.Request) string {
	t.Helper()
	var body []byte
	opts.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ = io.ReadAll(r.Body)
		// A minimal well-formed response: the stream's outcome is irrelevant,
		// only the request matters, but a malformed one would make the
		// provider retry and capture twice.
		return &http.Response{StatusCode: 200,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   io.NopCloser(strings.NewReader("{}"))}, nil
	})
	opts.Env = map[string]string{"ANTHROPIC_API_KEY": "test-key"}
	core.EventStreamOf(anthropic.Provider(*m, opts).Stream(context.Background(), req)).Result()
	if len(body) == 0 {
		t.Fatal("no request body was captured")
	}
	if !json.Valid(body) {
		t.Fatalf("provider sent a body that is not valid JSON:\n%s", body)
	}
	return string(body)
}

// TS-04-54: Populated Metadata on the canonical request's tool result leaves
// the request golden byte-identical.
func TestMetadataNotInRequestBodies_TS04_54(t *testing.T) {
	sentinels := []string{"agentkit-golden-sentinel", "spill_path", "exit_code", "duration_ms"}
	for _, tc := range goldenRequestCases(t) {
		for _, s := range sentinels {
			if strings.Contains(tc.body, s) {
				t.Errorf("%s request body contains %q", tc.name, s)
			}
		}
	}
}

func goldenRequestCases(t *testing.T) []goldenCase {
	t.Helper()
	return goldenCasesFor(t, canonicalRequest(t))
}

// goldenCasesFor captures req's body from the wire API.
func goldenCasesFor(t *testing.T, req core.Request) []goldenCase {
	t.Helper()
	noenv := func(string) string { return "" }
	return []goldenCase{
		{"anthropic", capture(t,
			anthropic.Options{BaseURL: "https://example.invalid", Getenv: noenv},
			canonicalModel(t), req)},
	}
}

// randomOutputSchema builds an arbitrary output schema: nested objects,
// arrays, enums, unions and optional fields, so a provider that reached for
// OutputSchema by any route would put some of it into the body.
func randomOutputSchema(r *rand.Rand, depth int) *schema.Schema {
	leaf := []func() *schema.Schema{
		func() *schema.Schema { return schema.String("out-sentinel string") },
		func() *schema.Schema { return schema.Int() },
		func() *schema.Schema { return schema.Number() },
		func() *schema.Schema { return schema.Bool() },
		func() *schema.Schema { return schema.Enum("out-sentinel enum", "ok", "exit") },
	}
	if depth <= 0 || r.Intn(3) == 0 {
		return leaf[r.Intn(len(leaf))]()
	}
	switch r.Intn(3) {
	case 0:
		return schema.Array(randomOutputSchema(r, depth-1))
	case 1:
		return schema.OneOf(randomOutputSchema(r, depth-1), randomOutputSchema(r, depth-1))
	}
	var fields []schema.Field
	for i := range 1 + r.Intn(4) {
		name := "out_sentinel_" + string(rune('a'+i))
		if r.Intn(2) == 0 {
			fields = append(fields, schema.Prop(name, randomOutputSchema(r, depth-1)))
		} else {
			fields = append(fields, schema.Opt(name, randomOutputSchema(r, depth-1)))
		}
	}
	return schema.Object(fields...)
}

// TS-06-3: whatever OutputSchema a tool declares — none, or any shape — the
// request body stays byte-identical to the checked-in golden. 06-REQ-1.4.
func TestOutputSchemaNeverChangesRequestBodies_TS06_3(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	for i := range 50 {
		var out *schema.Schema
		if i%5 != 0 {
			out = randomOutputSchema(r, 3)
		}
		req := canonicalRequest(t)
		req.Tools = core.ToolWires(canonicalTools(out))
		for _, tc := range goldenCasesFor(t, req) {
			want, err := os.ReadFile(filepath.Join("testdata", "golden", "request_"+tc.name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, []byte(tc.body)) {
				t.Fatalf("iteration %d: %s request body differs from its golden with OutputSchema %v",
					i, tc.name, out != nil)
			}
			if strings.Contains(tc.body, "out_sentinel") || strings.Contains(tc.body, "out-sentinel") {
				t.Fatalf("iteration %d: %s request body carries the output schema", i, tc.name)
			}
		}
	}
}

// TS-06-32 (smoke, 06-PATH-3): the canonical request's tool carries the real
// find_files OutputSchema from tools.All, is projected through
// core.ToolWires, and every provider's body still equals its golden.
func TestSmokeOutputSchemaToolsKeepGoldens_TS06_32(t *testing.T) {
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	var out *schema.Schema
	for _, tl := range all {
		if tl.Name == "find_files" {
			out = tl.OutputSchema
		}
	}
	if out == nil {
		t.Fatal("find_files declares no OutputSchema")
	}
	req := canonicalRequest(t)
	req.Tools = core.ToolWires(canonicalTools(out))
	cases := goldenCasesFor(t, req)
	if len(cases) != 1 {
		t.Fatalf("%d providers, want 1", len(cases))
	}
	for _, tc := range cases {
		want, err := os.ReadFile(filepath.Join("testdata", "golden", "request_"+tc.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, []byte(tc.body)) {
			t.Errorf("%s request body differs from its golden with an OutputSchema on the tool", tc.name)
		}
		if strings.Contains(tc.body, `"marker"`) {
			t.Errorf("%s request body carries the output schema", tc.name)
		}
	}
}

// goldenBytes is the checked-in request golden.
func goldenBytes(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "golden", "request_anthropic.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// goldenPayload is the golden decoded, keys in order lost but values kept.
func goldenPayload(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(goldenBytes(t), &out); err != nil {
		t.Fatalf("the golden is not JSON: %v", err)
	}
	return out
}

func hasBreakpoint(v any) bool {
	obj, _ := v.(map[string]any)
	cc, _ := obj["cache_control"].(map[string]any)
	return cc != nil && cc["type"] == "ephemeral"
}

func lastOf(v any) any {
	s, _ := v.([]any)
	if len(s) == 0 {
		return nil
	}
	return s[len(s)-1]
}

// TS-10-29: the encoder's body for the canonical request is the golden, byte
// for byte.
func TestGoldenRequestIsTheEncodedBody_TS10_29(t *testing.T) {
	body, err := anthropic.BuildRequestJSON(canonicalRequest(t), *canonicalModel(t))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, goldenBytes(t)) {
		t.Fatalf("the encoded body differs from testdata/golden/request_anthropic.json:\n got: %s\nwant: %s",
			body, goldenBytes(t))
	}
}

// TS-10-30: cache_control sits on exactly the last system block, the last
// tool, the prefix's last block and the last user block.
func TestGoldenBreakpoints_TS10_30(t *testing.T) {
	p := goldenPayload(t)
	if n := strings.Count(string(goldenBytes(t)), `"cache_control"`); n != 4 {
		t.Fatalf("%d cache_control markers, want exactly 4", n)
	}
	sys, _ := p["system"].([]any)
	if len(sys) < 2 || !hasBreakpoint(lastOf(sys)) || hasBreakpoint(sys[0]) {
		t.Fatalf("system = %v; want the breakpoint on the last block only", sys)
	}
	tools, _ := p["tools"].([]any)
	if len(tools) < 2 || !hasBreakpoint(lastOf(tools)) || hasBreakpoint(tools[0]) {
		t.Fatalf("tools = %v; want the breakpoint on the last tool only", tools)
	}
	msgs, _ := p["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("%d messages, want the 2-message prefix and 3 of history", len(msgs))
	}
	prefixEnd := msgs[1].(map[string]any)["content"]
	if !hasBreakpoint(lastOf(prefixEnd)) {
		t.Fatalf("the prefix's last block %v carries no breakpoint", lastOf(prefixEnd))
	}
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" || !hasBreakpoint(lastOf(last["content"])) {
		t.Fatalf("the last user block %v carries no breakpoint", lastOf(last["content"]))
	}
}

// TS-10-31: every tool is strict, and no internal tool field appears.
func TestGoldenToolsAreStrictAndClean_TS10_31(t *testing.T) {
	raw := string(goldenBytes(t))
	tools, _ := goldenPayload(t)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("the golden declares no tools")
	}
	for _, tl := range tools {
		if tl.(map[string]any)["strict"] != true {
			t.Errorf("tool %v is not strict", tl.(map[string]any)["name"])
		}
	}
	for _, field := range []string{"OutputSchema", "output_schema", "outputSchema", "ReachableTools",
		"reachable_tools", "reachable_child", "Terminating", "terminating", "accepted"} {
		if strings.Contains(raw, field) {
			t.Errorf("the golden carries %q", field)
		}
	}
}

// TS-10-32: an adaptive model's request carries thinking adaptive and an
// effort, and no budget.
func TestGoldenThinkingIsAdaptive_TS10_32(t *testing.T) {
	p := goldenPayload(t)
	th, _ := p["thinking"].(map[string]any)
	if th == nil || th["type"] != "adaptive" {
		t.Fatalf("thinking = %v, want type adaptive", p["thinking"])
	}
	oc, _ := p["output_config"].(map[string]any)
	if oc == nil || oc["effort"] == nil || oc["effort"] == "" {
		t.Fatalf("output_config = %v, want an effort", p["output_config"])
	}
	if strings.Contains(string(goldenBytes(t)), "budget_tokens") {
		t.Fatal("the golden carries budget_tokens")
	}
}

// TS-10-33: the replayed tool_use input is in the golden exactly as written.
func TestGoldenToolInputIsVerbatim_TS10_33(t *testing.T) {
	if !strings.Contains(string(goldenBytes(t)), `"input":`+canonicalToolInput) {
		t.Fatalf("the golden does not carry the tool input %s verbatim", canonicalToolInput)
	}
}

// TS-10-35 (smoke, 10-PATH-2): the catalog row and the encoder together
// produce the golden request, byte for byte. It lives beside the canonical
// request it encodes.
func TestSmokeGoldenRequestFromTheCatalog_TS10_35(t *testing.T) {
	req := canonicalRequest(t)
	model, ok := catalog.Lookup(canonicalModelID)
	if !ok {
		t.Fatal("model not found")
	}
	if model.ThinkingKind != core.ThinkingKindAdaptive {
		t.Fatalf("%s thinking = %q, want adaptive", canonicalModelID, model.ThinkingKind)
	}
	body, err := anthropic.BuildRequestJSON(req, model)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, goldenBytes(t)) {
		t.Fatalf("golden mismatch:\n got: %s\nwant: %s", body, goldenBytes(t))
	}
}
