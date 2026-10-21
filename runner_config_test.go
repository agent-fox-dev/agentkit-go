package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// fieldNames is the names of a struct type's fields, in order.
func fieldNames(typ reflect.Type) []string {
	names := make([]string, typ.NumField())
	for i := range names {
		names[i] = typ.Field(i).Name
	}
	return names
}

// TS-21-1: Toolset has exactly the five documented fields, in order.
func TestToolsetHasTheFiveFields_TS21_1(t *testing.T) {
	typ := reflect.TypeOf(Toolset{})
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"Tools", reflect.TypeOf([]core.Tool(nil))},
		{"Policy", reflect.TypeOf(core.ToolPolicy{})},
		{"Guard", reflect.TypeOf(core.BeforeToolCall(nil))},
		{"After", reflect.TypeOf(core.AfterToolCall(nil))},
		{"Events", reflect.TypeOf((func(core.Event))(nil))},
	}
	if typ.NumField() != len(want) {
		t.Fatalf("Toolset fields = %v, want %d of them", fieldNames(typ), len(want))
	}
	for i, w := range want {
		if f := typ.Field(i); f.Name != w.name || f.Type != w.typ {
			t.Errorf("Toolset field %d = %s %v, want %s %v", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

// TS-21-2: Config has exactly fourteen fields (twelve until 29-REQ-1.1 added Env
// and Getenv), in order, and none of the four moved tool fields.
func TestConfigHasFourteenFieldsAndNoToolFields_TS21_2(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	want := []string{"Client", "Provider", "Model", "Effort", "Env", "Getenv", "System", "Prefix", "Toolset",
		"MaxTurns", "MaxCostUSD", "Timeout", "Prune", "MaxTokens"}
	if got := fieldNames(typ); !slices.Equal(got, want) {
		t.Fatalf("Config fields = %v, want %v", got, want)
	}
	for _, gone := range []string{"Tools", "Policy", "Guard", "After"} {
		if _, ok := typ.FieldByName(gone); ok {
			t.Errorf("Config still has a %s field", gone)
		}
	}
	if f, _ := typ.FieldByName("Toolset"); f.Type != reflect.TypeOf(Toolset{}) {
		t.Errorf("Config.Toolset has type %v, want Toolset", f.Type)
	}
}

// runnerExec is a tool body for tests that only need a tool to exist.
func runnerExec(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }

// TS-21-4: NewRunner checks in today's order and returns the first failure; a
// valid toolset returns a runner.
func TestNewRunnerChecksInOrder_TS21_4(t *testing.T) {
	neither := core.Tool{Name: "neither"}
	shell := core.Tool{Name: "run_command", Handler: noopHandler}
	both := core.Tool{Name: "both", Handler: noopHandler, Execute: runnerExec}

	// The first tool is invalid and a shell tool is unguarded: the tool check
	// comes first.
	r, err := NewRunner(Toolset{Tools: []core.Tool{neither, shell}})
	if r != nil || err == nil || !strings.Contains(err.Error(), `has neither Handler nor Execute`) ||
		errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("NewRunner = %v, %v; want the neither-Handler-nor-Execute error", r, err)
	}

	// The registered tools are fine; the policy adds one that sets both. Only
	// the check on the resolved set can catch it.
	r, err = NewRunner(Toolset{
		Tools:  []core.Tool{{Name: "ok", Handler: noopHandler}},
		Policy: core.ToolPolicy{CustomTools: []core.Tool{both}},
	})
	if r != nil || err == nil || !strings.Contains(err.Error(), `tool "both" sets both Handler and Execute; exactly one`) {
		t.Fatalf("NewRunner = %v, %v; want the sets-both error from the resolved set", r, err)
	}

	r, err = NewRunner(Toolset{Tools: []core.Tool{shell}, Guard: allowAll})
	if r == nil || err != nil {
		t.Fatalf("NewRunner on a valid toolset = %v, %v; want a runner", r, err)
	}
}

// TS-21-5: a tool with neither or both of Handler and Execute is refused with
// the exact texts, directly and through the policy.
func TestNewRunnerRefusesBadHandlers_TS21_5(t *testing.T) {
	neither := core.Tool{Name: "t"}
	both := core.Tool{Name: "u", Handler: noopHandler, Execute: runnerExec}
	cases := []struct {
		name string
		ts   Toolset
		want string
	}{
		{"neither", Toolset{Tools: []core.Tool{neither}}, `agentkit: tool "t" has neither Handler nor Execute`},
		{"both", Toolset{Tools: []core.Tool{both}}, `agentkit: tool "u" sets both Handler and Execute; exactly one`},
		{"neither via policy", Toolset{Policy: core.ToolPolicy{CustomTools: []core.Tool{neither}}}, `agentkit: tool "t" has neither Handler nor Execute`},
		{"both via policy", Toolset{Policy: core.ToolPolicy{CustomTools: []core.Tool{both}}}, `agentkit: tool "u" sets both Handler and Execute; exactly one`},
		{"neither behind a wrapper", Toolset{Tools: []core.Tool{{Name: "w", Handler: noopHandler, ReachableTools: []core.Tool{neither}}}}, `agentkit: tool "t" has neither Handler nor Execute`},
		{"both behind a wrapper the policy adds", Toolset{Policy: core.ToolPolicy{CustomTools: []core.Tool{{Name: "w", Handler: noopHandler, ReachableTools: []core.Tool{both}}}}}, `agentkit: tool "u" sets both Handler and Execute; exactly one`},
	}
	for _, c := range cases {
		r, err := NewRunner(c.ts)
		if r != nil || err == nil || err.Error() != c.want {
			t.Errorf("%s: NewRunner = %v, %v; want a nil runner and %q", c.name, r, err, c.want)
		}
	}
}

// TS-21-6: a reachability cycle and a wrapper reaching a terminating tool are
// refused with the exact texts.
func TestNewRunnerRefusesCyclesAndTerminatingReach_TS21_6(t *testing.T) {
	a := core.Tool{Name: "a", Handler: noopHandler, ReachableTools: make([]core.Tool, 1)}
	b := core.Tool{Name: "b", Handler: noopHandler, ReachableTools: []core.Tool{a}}
	a.ReachableTools[0] = b
	r, err := NewRunner(Toolset{Tools: []core.Tool{a}})
	if r != nil || err == nil || err.Error() != `agentkit: reachable tools cycle detected: a -> b -> a` {
		t.Errorf("cycle: NewRunner = %v, %v", r, err)
	}

	x := core.Tool{Name: "x", Handler: noopHandler, Terminating: true}
	wrap := core.Tool{Name: "codemode", Handler: noopHandler, ReachableTools: []core.Tool{x}}
	r, err = NewRunner(Toolset{Tools: []core.Tool{wrap}})
	if r != nil || err == nil || err.Error() != `agentkit: terminating tool "x" cannot be reached through wrapper "codemode"` {
		t.Errorf("terminating: NewRunner = %v, %v", r, err)
	}
}

// TS-21-7: a reachable shell tool with a nil Guard is refused, and the error
// names Toolset.Guard.
func TestNewRunnerRefusesUnguardedShell_TS21_7(t *testing.T) {
	shell := core.Tool{Name: "run_command", Handler: noopHandler}
	wrap := core.Tool{Name: "wrap", Handler: noopHandler, ReachableTools: []core.Tool{shell}}
	cases := []struct {
		name   string
		tools  []core.Tool
		suffix string
	}{
		{"direct", []core.Tool{shell}, ` (tool "run_command")`},
		{"wrapper", []core.Tool{wrap}, ` (wrapper "wrap" reached shell tool "run_command")`},
	}
	for _, c := range cases {
		r, err := NewRunner(Toolset{Tools: c.tools})
		switch {
		case r != nil || err == nil:
			t.Errorf("%s: NewRunner = %v, %v; want a nil runner and an error", c.name, r, err)
		case !errors.Is(err, core.ErrUnguardedExecute):
			t.Errorf("%s: %v does not wrap ErrUnguardedExecute", c.name, err)
		case !strings.Contains(err.Error(), "Toolset.Guard is nil") || strings.Contains(err.Error(), "Config.Guard"):
			t.Errorf("%s: %q should name Toolset.Guard and not Config.Guard", c.name, err)
		case !strings.HasSuffix(err.Error(), c.suffix):
			t.Errorf("%s: %q does not end with %q", c.name, err, c.suffix)
		}
	}
}

// genToolset builds a toolset from a pool of tools that pass and fail each of
// NewRunner's checks, under a random guard and policy.
func genToolset(r *rand.Rand) Toolset {
	done := core.Tool{Name: "done", Handler: noopHandler, Terminating: true}
	shell := core.Tool{Name: "run_command", Handler: noopHandler}
	pool := []core.Tool{
		{Name: "alpha", Handler: noopHandler},
		{Name: "beta", Execute: runnerExec},
		{Name: "neither"},
		{Name: "both", Handler: noopHandler, Execute: runnerExec},
		cyclicPair(),
		shell,
		{Name: "execute", Handler: noopHandler},
		{Name: "wrap_done", Handler: noopHandler, ReachableTools: []core.Tool{done}},
		{Name: "wrap_shell", Handler: noopHandler, ReachableTools: []core.Tool{shell}},
		done,
	}
	pick := func(n int) []core.Tool {
		var out []core.Tool
		for range n {
			out = append(out, pool[r.Intn(len(pool))])
		}
		return out
	}
	// A reachability cycle that enters only through the policy's CustomTools
	// is never refused: core.ToolPolicy.Resolve recurses on it before the
	// check on the resolved set can run, as it did in New before NewRunner
	// existed. That is outside this spec, so the generator keeps cycles to
	// Tools, where the first check refuses them.
	pickAcyclic := func(n int) []core.Tool {
		var out []core.Tool
		for len(out) < n {
			if t := pool[r.Intn(len(pool))]; t.Name != "toolA" {
				out = append(out, t)
			}
		}
		return out
	}
	names := func(tools []core.Tool) []string {
		var out []string
		for _, t := range tools {
			out = append(out, t.Name)
		}
		return out
	}
	ts := Toolset{Tools: pick(r.Intn(5))}
	if r.Intn(2) == 0 {
		ts.Guard = allowAll
	}
	switch r.Intn(6) {
	case 1:
		ts.Policy.ToolNames = names(pick(2))
	case 2:
		ts.Policy.ExcludeTools = names(pick(2))
	case 3:
		ts.Policy.NoTools = core.NoToolsAll
	case 4:
		ts.Policy.CustomTools = pickAcyclic(2)
	case 5:
		ts.Policy.Tools = pick(r.Intn(3))
	}
	return ts
}

// TS-21-8: NewRunner and New report the same error, with the same text, for
// any toolset.
func TestNewRunnerAndNewAgreeOnEveryToolset_TS21_8(t *testing.T) {
	r := rand.New(rand.NewSource(21))
	var refused, accepted int
	for i := range 400 {
		ts := genToolset(r)
		_, e1 := NewRunner(ts)
		_, e2 := New(Config{Client: faux.New(), Model: testModelID, Toolset: ts})
		if (e1 == nil) != (e2 == nil) {
			t.Fatalf("iteration %d: NewRunner err = %v, New err = %v; want both or neither", i, e1, e2)
		}
		if e1 == nil {
			accepted++
			continue
		}
		refused++
		if e1.Error() != e2.Error() {
			t.Fatalf("iteration %d: NewRunner says %q, New says %q", i, e1, e2)
		}
	}
	if refused == 0 || accepted == 0 {
		t.Fatalf("the generator made %d refused and %d accepted toolsets; it must make both", refused, accepted)
	}
}

// TS-21-9: changing the caller's Tools slice after NewRunner does not change
// the runner.
func TestNewRunnerCopiesTools_TS21_9(t *testing.T) {
	alpha := core.Tool{Name: "alpha", Handler: noopHandler}
	beta := core.Tool{Name: "beta", Handler: noopHandler}
	tools := []core.Tool{alpha, beta}
	r, err := NewRunner(Toolset{Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	tools[0] = core.Tool{Name: "other", Handler: noopHandler}
	_ = append(tools, core.Tool{Name: "gamma", Handler: noopHandler})
	tools[1].Name = "renamed"
	var got []string
	for _, tl := range r.Tools() {
		got = append(got, tl.Name)
	}
	if !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("Tools() = %v after the caller changed its slice, want [alpha beta]", got)
	}
}

// consumerFiles are the files TS-21-49 scans: every root test, and the other
// tests and examples that build an agentkit.Config.
func consumerFiles(t *testing.T) (root, qualified []string) {
	t.Helper()
	root, err := filepath.Glob("*_test.go")
	if err != nil || len(root) == 0 {
		t.Fatalf("no root test files: %v", err)
	}
	qualified = []string{
		"codemode/smoke_test.go",
		"sandbox/integration_test.go",
		"sandbox/codemode_smoke_test.go",
		"examples/harness/harness.go",
	}
	mains, err := filepath.Glob("examples/*/main.go")
	if err != nil {
		t.Fatal(err)
	}
	return root, append(qualified, mains...)
}

// TS-21-49: no consumer sets Tools, Policy, Guard or After directly on a
// Config literal. The compiler enforces the same for field selectors, so a
// stale `cfg.Guard = …` is a build error in every module.
func TestNoConsumerSetsTheRemovedConfigFields_TS21_49(t *testing.T) {
	removed := map[string]bool{"Tools": true, "Policy": true, "Guard": true, "After": true}
	rootFiles, qualifiedFiles := consumerFiles(t)
	scan := func(path string, isConfig func(ast.Expr) bool) {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || lit.Type == nil || !isConfig(lit.Type) {
				return true
			}
			for _, e := range lit.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok && removed[k.Name] {
						t.Errorf("%s: a Config literal sets %s directly; it belongs in Toolset", path, k.Name)
					}
				}
			}
			return true
		})
	}
	bare := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == "Config" }
	qual := func(e ast.Expr) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && x.Name == "agentkit" && s.Sel.Name == "Config"
	}
	for _, f := range rootFiles {
		scan(f, bare)
	}
	for _, f := range qualifiedFiles {
		scan(f, qual)
	}
	if t.Failed() {
		t.Logf("scanned %d root files and %d others", len(rootFiles), len(qualifiedFiles))
	}
}
