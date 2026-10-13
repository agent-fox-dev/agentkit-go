package sandbox_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-1: GitRepo is a sealed Source with exactly URL, Ref and Depth, and
// ErrNoAgent carries its exact message and matches through wrapping.
// 17-REQ-1.1.
func TestGitRepoAndErrNoAgent_TS17_1(t *testing.T) {
	var src sandbox.Source = sandbox.GitRepo{URL: "https://h/r", Ref: "main", Depth: 1}
	if _, ok := src.(sandbox.GitRepo); !ok {
		t.Fatalf("Source holds %T, want sandbox.GitRepo", src)
	}
	typ := reflect.TypeFor[sandbox.GitRepo]()
	want := []struct {
		name string
		kind reflect.Kind
	}{{"URL", reflect.String}, {"Ref", reflect.String}, {"Depth", reflect.Int}}
	if typ.NumField() != len(want) {
		t.Fatalf("GitRepo has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, w := range want {
		if f := typ.Field(i); f.Name != w.name || f.Type.Kind() != w.kind {
			t.Errorf("field %d is %s %s, want %s %s", i, f.Name, f.Type.Kind(), w.name, w.kind)
		}
	}
	if got := sandbox.ErrNoAgent.Error(); got != "sandbox: the image has no executable agent at sandbox.AgentPath" {
		t.Errorf("ErrNoAgent = %q", got)
	}
	if !errors.Is(fmt.Errorf("x: %w", sandbox.ErrNoAgent), sandbox.ErrNoAgent) {
		t.Error("a wrapped ErrNoAgent does not match")
	}
}
