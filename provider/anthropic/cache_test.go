package anthropic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func namedTool(name string) core.ToolWire {
	return core.ToolWire{Name: name, InputSchema: schema.Object(schema.Opt("x", schema.String()))}
}

// ttls renders every breakpoint of a body, "-" for none: the system blocks,
// the tools, then each message's blocks, separated by " / ".
func ttls(body map[string]any) string {
	mark := func(bs []map[string]any) string {
		var out []string
		for _, b := range bs {
			out = append(out, ttl(b))
		}
		return strings.Join(out, ",")
	}
	parts := []string{mark(objects(body["system"])), mark(objects(body["tools"]))}
	for _, bs := range blocks(body) {
		parts = append(parts, mark(bs))
	}
	return strings.Join(parts, " / ")
}

// One breakpoint each sits on the last system block, the last tool, the
// prefix's last block and a final user turn's last block, with the hour TTL
// only on the stable ones and only where the row supports it.
func TestBreakpointsSitAtTheEndOfEachCachedSection(t *testing.T) {
	req := core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: "one"}, core.TextBlock{Text: "two"}, core.TextBlock{Text: "three"}},
		Tools:  []core.ToolWire{namedTool("a"), namedTool("b")},
		Prefix: core.Messages{user("brief", "spec"), reply(core.TextBlock{Text: "understood"})},
		Messages: core.Messages{
			user("q"), reply(toolUse("c1", "a", `{}`)), result("c1", "ok"), user("first", "last"),
		},
	}
	const layout = "-,-,P / -,P / -,- / P / - / - / -,-,R"
	noLong := testModel()
	noLong.Compat = json.RawMessage(`{"supports_long_cache_retention": false}`)
	cases := []struct {
		retention       core.CacheRetention
		model           *core.Model
		prefix, rolling string
	}{
		{core.CacheRetentionShort, testModel(), "5m", "5m"},
		{core.CacheRetentionLong, testModel(), "1h", "5m"},
		{core.CacheRetentionLong, noLong, "5m", "5m"},
		{core.CacheRetentionNone, testModel(), "-", "-"},
	}
	for _, c := range cases {
		want := strings.NewReplacer("P", c.prefix, "R", c.rolling).Replace(layout)
		if got := ttls(wireBody(t, c.model, req, anthropic.Options{CacheRetention: c.retention})); got != want {
			t.Errorf("%s (compat %s): breakpoints\n got %s\nwant %s", c.retention, c.model.Compat, got, want)
		}
	}

	// The rolling breakpoint is for a user turn's text or result; an
	// assistant turn at the end carries none.
	body := wireBody(t, testModel(), core.Request{Messages: core.Messages{user("q"), reply(core.TextBlock{Text: "a"})}},
		anthropic.Options{})
	if got := ttls(body); got != " /  / - / -" {
		t.Errorf("breakpoints = %q", got)
	}
}

// A prefix that ends on the role the history starts with is joined to it, and
// the breakpoint stays on the prefix's own last block.
func TestAPrefixJoinsAHistoryThatStartsWithItsLastRole(t *testing.T) {
	body := wireBody(t, testModel(), core.Request{
		Prefix:   core.Messages{user("brief")},
		Messages: core.Messages{user("question"), reply(core.TextBlock{Text: "answer"})},
	}, anthropic.Options{})
	if got := blockTypes(body); got != "user:text,text assistant:text" {
		t.Fatalf("messages = %s", got)
	}
	if got := ttls(body); got != " /  / 5m,- / -" {
		t.Errorf("breakpoints = %q, want one on the prefix's last block", got)
	}
}

// A tool that appeared mid-session goes after the established ones with no
// breakpoint, so the cached prefix is byte-identical to the last turn's, and
// stays visible to the model: no defer_loading.
func TestALateAddedToolIsAppendedAfterTheCachedTools(t *testing.T) {
	history := core.Messages{
		user("go"), reply(toolUse("c1", "read_file", `{}`)),
		core.ToolResultMessage{ToolUseID: "c1", ToolName: "read_file", Content: core.Content{core.TextBlock{Text: "ok"}},
			AddedToolNames: []string{"mcp__db__query"}},
	}
	for _, c := range []struct {
		name    string
		history core.Messages
		want    string
	}{
		{"a late tool", history, "read_file:5m mcp__db__query:-"},
		{"nothing deferred", nil, "mcp__db__query:- read_file:5m"},
	} {
		body := wireBody(t, testModel(), core.Request{
			Tools: []core.ToolWire{namedTool("mcp__db__query"), namedTool("read_file")}, Messages: c.history,
		}, anthropic.Options{})
		var got []string
		for _, tl := range objects(body["tools"]) {
			if _, ok := tl["defer_loading"]; ok {
				t.Errorf("%s: %v carries defer_loading, which hides it from the model", c.name, tl["name"])
			}
			got = append(got, tl["name"].(string)+":"+ttl(tl))
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: tools = %s, want %s", c.name, strings.Join(got, " "), c.want)
		}
	}
}

// The tool schemas are serialized once per session: a steady-state turn
// marshals none, and each reconciliation is reported to the embedder.
func TestToolSchemasAreSerializedOncePerSession(t *testing.T) {
	req := core.Request{Messages: core.Messages{user("hi")}, Tools: []core.ToolWire{namedTool("a"), namedTool("b")}}
	var prefix provider.ToolPrefix
	for turn, want := range []int{2, 0} {
		_, _, rep, err := anthropic.BuildRequestCached(testModel(), req, core.CacheRetentionShort, &prefix)
		if err != nil || rep.Marshalled != want {
			t.Fatalf("turn %d: report %+v, %v; want %d marshalled", turn, rep, err, want)
		}
	}

	var reports []provider.SyncReport
	opts := anthropic.Options{ToolPrefix: &provider.ToolPrefix{},
		OnToolPrefixSync: func(r provider.SyncReport) { reports = append(reports, r) }}
	run(t, testModel(), req, opts, 200, streamFixture())
	run(t, testModel(), req, opts, 200, streamFixture())
	if len(reports) != 2 || len(reports[0].Added) != 2 || reports[1].Marshalled != 0 {
		t.Fatalf("reports = %+v, want both tools added and then none marshalled", reports)
	}
}
