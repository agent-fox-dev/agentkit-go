package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestWireProjectsOnlyWhatAProviderMaySee(t *testing.T) {
	in := schema.Object(schema.Prop("q", schema.String()))
	tl := Tool{Name: "search", Description: "d", InputSchema: in, OutputSchema: schema.Object(), Label: "Search",
		PromptGuidelines: []string{"never sent"}, ExecutionMode: Sequential, Terminating: true,
		ReachableTools: []Tool{{Name: "child"}}}
	want := ToolWire{Name: "search", Description: "d", InputSchema: in}
	if w := tl.Wire(); w != want {
		t.Fatalf("Wire() = %+v, want %+v", w, want)
	}
	if ws := ToolWires([]Tool{tl, {Name: "b"}}); len(ws) != 2 || ws[0] != want || ws[1].Name != "b" {
		t.Fatalf("ToolWires = %+v", ws)
	}
}

func TestToolResultLLMTextIsTheRenderedTextOrTheEnvelope(t *testing.T) {
	exit := 1
	for _, tc := range []struct {
		name string
		r    ToolResult
		want string
	}{
		{"rendered text wins", ToolResult{OK: true, Data: map[string]any{"k": 1}, Text: "line 1\nline 2"}, "line 1\nline 2"},
		{"ok envelope", OKResult(map[string]any{"k": 1}), `{"data":{"k":1},"ok":true}`},
		{"error envelope", ErrResult("not_found", "no such file"), `{"detail":"no such file","error":"not_found","ok":false}`},
		{"metadata and the terminate vote stay out", ToolResult{OK: true, Terminate: true, Metadata: &ToolMetadata{ExitCode: &exit}}, `{"ok":true}`},
		{"an unmarshalable value", OKResult(map[string]any{"x": math.NaN()}), `{"ok":false,"error":"marshal_failed"}`},
	} {
		if got := tc.r.LLMText(); got != tc.want {
			t.Errorf("%s: LLMText() = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestToolMetadataCloneIsDeepAndIsEmptyMeansNoInformation(t *testing.T) {
	var none *ToolMetadata
	if none.Clone() != nil || !none.IsEmpty() || !(&ToolMetadata{DurationMS: 0}).IsEmpty() {
		t.Fatal("nil and all-zero metadata are empty, and nil clones to nil")
	}
	zero := 0
	for _, m := range []*ToolMetadata{{ExitCode: &zero}, {Truncated: true}, {TruncatedBy: "lines"}, {TotalBytes: 1},
		{TotalLines: 1}, {SpillPath: "p"}, {DurationMS: 1}, {Outcome: "ok"}, {LineEnding: "lf"}} {
		if m.IsEmpty() {
			t.Errorf("%+v reported empty", *m)
		}
	}
	two := 2
	full := &ToolMetadata{ExitCode: &two, Outcome: "exit", TotalBytes: 5}
	c := full.Clone()
	if c == full || c.ExitCode == full.ExitCode || !reflect.DeepEqual(c, full) {
		t.Fatalf("Clone() = %+v, want an equal copy with its own ExitCode", c)
	}
	*c.ExitCode, c.Outcome = 9, "changed"
	if *full.ExitCode != 2 || full.Outcome != "exit" {
		t.Fatal("mutating the clone changed the original")
	}
}

func TestBatchTerminatesOnlyWhenEveryResultVotesSo(t *testing.T) {
	for _, c := range []struct {
		votes []bool
		want  bool
	}{{nil, false}, {[]bool{true}, true}, {[]bool{true, false}, false}, {[]bool{false, true}, false}, {[]bool{true, true}, true}} {
		if got := BatchTerminates(c.votes); got != c.want {
			t.Errorf("BatchTerminates(%v) = %v, want %v", c.votes, got, c.want)
		}
	}
}

func TestReachableToolsIsTheDepthFirstClosureEachNameOnce(t *testing.T) {
	d := Tool{Name: "D"}
	b := Tool{Name: "B", ReachableTools: []Tool{d}}
	c := Tool{Name: "C", ReachableTools: []Tool{d}}
	a := Tool{Name: "A", ReachableTools: []Tool{b, c}}
	if got := names(ReachableTools([]Tool{{Name: "leaf"}, a, c})); !reflect.DeepEqual(got, []string{"leaf", "A", "B", "D", "C"}) {
		t.Fatalf("ReachableTools = %v", got)
	}
	if got := ReachableTools(nil); len(got) != 0 {
		t.Fatalf("ReachableTools(nil) = %v", got)
	}
}

func TestNamedPicksTopLevelToolsInNamesOrderAsCopies(t *testing.T) {
	ts := append(toolSet("read_file", "write_file", "list_files"), Tool{Name: "wrapper", ReachableTools: []Tool{{Name: "hidden"}}})
	orig := append([]Tool(nil), ts...)
	got, err := Named(ts, "list_files", "read_file")
	if err != nil || !reflect.DeepEqual(names(got), []string{"list_files", "read_file"}) {
		t.Fatalf("Named = %v, %v", names(got), err)
	}
	got[0].Description = "changed"
	if !reflect.DeepEqual(ts, orig) {
		t.Fatal("the set changed through the result")
	}
	if got, err := Named(ts, "hidden"); got != nil || err == nil {
		t.Fatalf("Named(hidden) = %v, %v; a wrapper's tools are not searched", got, err)
	}
	dup := []Tool{{Name: "dup", Description: "first"}, {Name: "dup", Description: "second"}}
	if d, err := Named(dup, "dup"); err != nil || len(d) != 1 || d[0].Description != "first" {
		t.Fatalf("Named(dup) = %+v, %v; want the first", d, err)
	}
	for _, set := range [][]Tool{ts, nil} {
		if got, err := Named(set); got == nil || len(got) != 0 || err != nil {
			t.Errorf("Named() = %#v, %v; want an empty non-nil slice", got, err)
		}
	}
}

func TestNamedNamesEveryMissingAndTheFirstRepeatedName(t *testing.T) {
	ts := toolSet("read_file", "write_file")
	for _, tc := range []struct {
		names []string
		set   []Tool
		want  string
	}{
		{[]string{"no_such_tool"}, ts, `core: no tool named "no_such_tool"; the set holds: read_file, write_file`},
		{[]string{"write_file", "b_missing", "a_missing"}, ts, `core: no tool named "b_missing", "a_missing"; the set holds: read_file, write_file`},
		{[]string{"x"}, nil, `core: no tool named "x"; the set is empty`},
		{[]string{"read_file", "write_file", "write_file", "read_file"}, ts, `core: tool "write_file" is named more than once`},
	} {
		got, err := Named(tc.set, tc.names...)
		if got != nil || err == nil || err.Error() != tc.want {
			t.Errorf("Named(%v) = %v, %v; want %q", tc.names, got, err, tc.want)
		}
	}
}

// Over random sets and picks, sequentially and concurrently.
func TestNamedReturnsAnyDistinctPickInOrder(t *testing.T) {
	rng := rand.New(rand.NewPCG(22, 6))
	for range 200 {
		n := 1 + rng.IntN(12)
		all := make([]string, n)
		for i := range all {
			all[i] = fmt.Sprintf("tool_%d", i)
		}
		ts := toolSet(all...)
		picks := append([]string{}, all...)
		rng.Shuffle(n, func(i, j int) { picks[i], picks[j] = picks[j], picks[i] })
		picks = picks[:rng.IntN(n+1)]
		got, err := Named(ts, picks...)
		if err != nil || !reflect.DeepEqual(names(got), picks) {
			t.Fatalf("Named(%v) = %v, %v", picks, names(got), err)
		}
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if r, _ := Named(ts, picks...); !reflect.DeepEqual(r, got) {
					t.Errorf("concurrent Named(%v) = %v", picks, names(r))
				}
			})
		}
		wg.Wait()
	}
}

type recordingCaller struct{ got []ToolUseBlock }

func (f *recordingCaller) Call(_ context.Context, calls ...ToolUseBlock) ([]ToolResult, error) {
	f.got = append(f.got, calls...)
	out := make([]ToolResult, len(calls))
	for i, c := range calls {
		out[i] = OKResult(map[string]any{"name": c.Name})
	}
	return out, nil
}

func TestCallNestedRunsThroughTheAttachedCallerOrNotAtAll(t *testing.T) {
	f := &recordingCaller{}
	res, err := CallNested(WithNestedCaller(context.Background(), f), ToolUseBlock{ID: "1", Name: "t1"}, ToolUseBlock{ID: "2", Name: "t2"})
	if err != nil || len(res) != 2 || res[1].Data["name"] != "t2" || len(f.got) != 2 || f.got[0].Name != "t1" {
		t.Fatalf("CallNested = %+v, %v; caller got %+v", res, err, f.got)
	}
	for _, ctx := range []context.Context{context.Background(), WithNestedCaller(context.Background(), nil)} {
		if res, err := CallNested(ctx, ToolUseBlock{Name: "child"}); res != nil || !errors.Is(err, ErrNoNestedCaller) {
			t.Fatalf("CallNested without a caller = %+v, %v", res, err)
		}
	}
}
