package core

import (
	"context"
	"errors"
	"testing"
)

type fakeCaller struct{ got []ToolUseBlock }

func (f *fakeCaller) Call(_ context.Context, calls ...ToolUseBlock) ([]ToolResult, error) {
	f.got = append(f.got, calls...)
	out := make([]ToolResult, len(calls))
	for i, c := range calls {
		out[i] = OKResult(map[string]any{"name": c.Name})
	}
	return out, nil
}

// TS-07-14: CallNested dispatches to the caller WithNestedCaller attached.
func TestNestedCallerContext_TS07_14(t *testing.T) {
	var _ NestedCaller = (*fakeCaller)(nil)
	f := &fakeCaller{}
	ctx := WithNestedCaller(context.Background(), f)
	res, err := CallNested(ctx, ToolUseBlock{ID: "call_1", Name: "t1"}, ToolUseBlock{ID: "call_2", Name: "t2"})
	if err != nil || len(res) != 2 || res[1].Data["name"] != "t2" {
		t.Fatalf("CallNested = %+v, %v", res, err)
	}
	if len(f.got) != 2 || f.got[0].Name != "t1" {
		t.Fatalf("caller received %+v", f.got)
	}
}

// TS-07-15: with no caller attached, CallNested runs nothing and says so.
func TestNestedCallerMissing_TS07_15(t *testing.T) {
	res, err := CallNested(context.Background(), ToolUseBlock{ID: "call_1", Name: "child"})
	if res != nil || !errors.Is(err, ErrNoNestedCaller) {
		t.Fatalf("CallNested = %+v, %v, want nil and ErrNoNestedCaller", res, err)
	}
}
