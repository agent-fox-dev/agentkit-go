package tools

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// goRunner declares Runner.Run, calls it through a renamed import and from a
// test, and calls an unrelated Run.
var goRunner = map[string]string{
	"go.mod": "module example.com/app\n\ngo 1.22\n",
	"runner/runner.go": "package runner\n\nimport \"fmt\"\n\ntype Runner struct{ Name string }\n\n" +
		"func (r *Runner) Run() error {\n\tfmt.Println(r.Name)\n\treturn nil\n}\n\ntype Job interface{ Run() error }\n",
	"other/other.go": "package other\n\ntype Other struct{}\n\nfunc (Other) Run() error { return nil }\n",
	"cmd/main.go": "package main\n\nimport (\n\trn \"example.com/app/runner\"\n\t\"example.com/app/other\"\n)\n\n" +
		"func start() error {\n\tr := &rn.Runner{Name: \"x\"}\n\treturn r.Run()\n}\n\n" +
		"func unrelated() error {\n\treturn other.Other{}.Run()\n}\n\nfunc main() { _ = start(); _ = unrelated() }\n",
	"runner/runner_test.go": "package runner\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) {\n\tr := &Runner{}\n\tif err := r.Run(); err != nil {\n\t\tt.Fatal(err)\n\t}\n}\n",
}

func refResult(t *testing.T, ft *fileTools, args string) ReferenceResult {
	t.Helper()
	return mustOK(t, call(ft.findReferencesTool(), args)).Data["result"].(ReferenceResult)
}

func TestFindReferencesRejectsBadArguments(t *testing.T) {
	ws := newWS(t, map[string]string{"main.go": "package main\nfunc Foo() {}\n"})
	fr := newFT(ws, Options{}).findReferencesTool()
	kinds := "kind must be one of: func, method, type, class, interface, enum, trait, const, var, module, macro"
	for args, want := range map[string]string{
		`{bad-json`:      "invalid_arguments",
		`{}`:             "invalid_arguments: name is required and must be non-empty",
		`{"name":"   "}`: "invalid_arguments: name is required and must be non-empty",
		`{"name":"` + strings.Repeat("a", 257) + `"}`: "invalid_arguments: name must be at most 256 bytes",
		`{"name":"Foo","kind":"struct"}`:              "invalid_arguments: " + kinds,
		`{"name":"Foo","path":"../out"}`:              "path_not_allowed",
		`{"name":"Foo","path":"missing"}`:             "invalid_arguments: directory not found: missing",
		`{"name":"Foo","path":"main.go"}`:             "invalid_arguments: path is not a directory: main.go",
	} {
		r := call(fr, args)
		code, text, _ := strings.Cut(want, ": ")
		if r.Error != code || r.Text == "" || (text != "" && r.Text != text) {
			t.Errorf("%.50s: %q %q, want %q", args, r.Error, r.Text, want)
		}
	}
	if r := call(fr, `{bad-json`); !strings.HasPrefix(r.Text, "Malformed input: ") {
		t.Errorf("malformed JSON: %q", r.Text)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := fr.Execute(ctx, []byte(`{"name":"Foo"}`)); r.Error != "aborted" || r.Text != "Operation aborted" {
		t.Errorf("a cancelled call: %+v", r)
	}
}

func TestFindReferencesResolvesAGoMethodsCallers(t *testing.T) {
	ws := newWS(t, goRunner)
	ft := newFT(ws, Options{})
	r := mustOK(t, call(ft.findReferencesTool(), `{"name":"Runner.Run"}`))
	res := r.Data["result"].(ReferenceResult)
	if res.Target.Name != "Run" || res.Target.Container != "Runner" || res.Target.StartLine != 7 || res.Backend != "go/types" {
		t.Fatalf("target %+v backend %q", res.Target, res.Backend)
	}
	// The call through the renamed import, and not the unrelated Other.Run.
	if got := siteList(res); !slices.Contains(got, "cmd/main.go:10:11 resolved") || slices.Contains(got, "cmd/main.go:14:23 resolved") {
		t.Fatalf("sites %v", got)
	}
	if s := res.Sites[0]; s.Enclosing.Name != "start" || s.Enclosing.Kind != outline.KindFunc || s.Source != "return r.Run()" {
		t.Fatalf("the call is attributed to %+v, source %q", s.Enclosing, s.Source)
	}
	if want := "find_references Runner.Run  (go/types, 2 references in 2 files; 0 text matches)\ncmd/main.go\n  L10  resolved  func start() error      return r.Run()\n" +
		"runner/runner_test.go\n  L7  resolved  func TestRun(t *testing.T)      if err := r.Run(); err != nil {"; r.Text != want {
		t.Fatalf("Text:\n%s\nwant:\n%s", r.Text, want)
	}
	if got := siteList(refResult(t, ft, `{"name":"Runner.Run","include_tests":false}`)); !slices.Equal(got, []string{"cmd/main.go:10:11 resolved"}) {
		t.Fatalf("include_tests false: %v", got)
	}
	// The tool and References agree.
	viaTool := refResult(t, ft, `{"name":"Runner.Run","include_tests":true,"max_results":30}`)
	direct, err := References(context.Background(), ws, viaTool.Target, ReferenceOptions{IncludeTests: true, MaxResults: 30})
	if err != nil || !reflect.DeepEqual(direct.Sites, viaTool.Sites) || direct.Backend != viaTool.Backend {
		t.Fatalf("References %v, the tool %v", siteList(direct), siteList(viaTool))
	}
	// An absolute path scope and a kind filter.
	if res := refResult(t, ft, `{"name":"Run","kind":"method","path":"`+abs(ws, "runner")+`"}`); res.Backend != "go/types" ||
		res.Target.Container != "Runner" || !slices.Equal(siteList(res), []string{"runner/runner_test.go:7:14 resolved"}) {
		t.Fatalf("a scoped, kind-filtered call: %+v", res)
	}
	// A caller written by write_file is found by the next call.
	mustOK(t, call(ft.writeFile(), `{"path":"cmd/extra.go","content":"package main\n\nimport rn \"example.com/app/runner\"\n\nfunc again() error {\n\treturn (&rn.Runner{}).Run()\n}\n"}`))
	if got := siteList(refResult(t, ft, `{"name":"Runner.Run"}`)); !slices.Contains(got, "cmd/extra.go:6:24 resolved") || len(got) != 3 {
		t.Fatalf("after write_file: %v", got)
	}
}

// A path scopes declaration lookup the same way it scopes reported sites: a
// path holding only callers, not the declaration, finds no candidate and
// falls back to a workspace-wide text search. Documented at
// docs/architecture.md's "Symbol and reference navigation" section.
func TestFindReferencesPathExcludingTheDeclarationFallsBackToText(t *testing.T) {
	ws := newWS(t, goRunner)
	ft := newFT(ws, Options{})
	// "cmd" calls Runner.Run but does not declare it; "runner" does.
	res := refResult(t, ft, `{"name":"Runner.Run","path":"cmd"}`)
	if res.Target != (outline.Decl{}) || res.Backend != "text" {
		t.Fatalf("target %+v backend %q, want a zero target and backend text", res.Target, res.Backend)
	}
	// The whole-workspace lookup the same name without path resolves via go/types.
	if unscoped := refResult(t, ft, `{"name":"Runner.Run"}`); unscoped.Backend != "go/types" || unscoped.Target.Name != "Run" {
		t.Fatalf("unscoped target %+v backend %q", unscoped.Target, unscoped.Backend)
	}
}

func TestFindReferencesOfANameNothingDeclaresIsText(t *testing.T) {
	ft := newFT(newWS(t, map[string]string{
		"config/settings.py": "ENABLED = NONEXISTENT_FLAG or False\n",
		"docs/notes.md":      "Set NONEXISTENT_FLAG to turn it on.\n",
		"main.go":            "package main\n\nfunc main() { _ = \"NONEXISTENT_FLAG\" }\n",
	}), Options{})
	r := mustOK(t, call(ft.findReferencesTool(), `{"name":"NONEXISTENT_FLAG"}`))
	res := r.Data["result"].(ReferenceResult)
	if res.Target != (outline.Decl{}) || r.Data["target"] != (outline.Decl{}) || res.Backend != "text" || len(res.Sites) != 3 ||
		!strings.HasPrefix(r.Text, "find_references NONEXISTENT_FLAG  (text, 3 references in 3 files; 3 text matches) [0 declarations matched]\n") {
		t.Fatalf("%+v\n%s", res, r.Text)
	}
	if r := mustOK(t, call(newFT(newWS(t, nil), Options{}).findReferencesTool(), `{"name":"GOPHER_KEY"}`)); !strings.Contains(r.Text, "(text, 0 references in 0 files; 0 text matches) [0 declarations matched]") {
		t.Fatalf("an empty workspace: %q", r.Text)
	}
}

func TestFindReferencesInPythonIsLexical(t *testing.T) {
	ws := newWS(t, map[string]string{
		"billing/tax.py":  "def calculate_tax(amount):\n    return amount * 0.2\n",
		"billing/shop.py": "# calculate_tax is applied at checkout\ndef checkout(total):\n    label = \"calculate_tax\"\n    return calculate_tax(total)\n",
	})
	res := refResult(t, newFT(ws, Options{}), `{"name":"calculate_tax"}`)
	if f, _ := outline.Outline(context.Background(), abs(ws, "billing/shop.py"), nil, outline.Options{}); f.Backend == outline.BackendNone {
		// Without cgo there is no Python grammar: nothing is declared, all is text.
		if res.Backend != "text" || slices.ContainsFunc(res.Sites, func(s ReferenceSite) bool { return s.Confidence != "text" }) {
			t.Fatalf("without a Python outline: %s %v", res.Backend, siteList(res))
		}
		return
	}
	// The call is code; the comment and the string are text.
	want := []string{"billing/shop.py:4:12 lexical", "billing/tax.py:1:5 lexical", "billing/shop.py:1:3 text", "billing/shop.py:3:14 text"}
	if res.Target.StartLine != 1 || res.Backend != "lexical" || !slices.Equal(siteList(res), want) {
		t.Fatalf("%+v: %v, want %v", res.Target, siteList(res), want)
	}
	if call, top := res.Sites[0].Enclosing, res.Sites[2].Enclosing; call.Name != "checkout" || call.StartLine != 2 || top.Kind != "file" {
		t.Fatalf("attributed to %+v and %+v", call, top)
	}
}

func TestABoundedReferencePassIsPartial(t *testing.T) {
	ws := newWS(t, map[string]string{"a.py": "def calc_total(x):\n    return x\n", "b.py": "calc_total(1)\n", "c.py": "calc_total(2)\n", "d.py": "calc_total(3)\n"})
	for reason, opts := range map[string]SymbolOptions{"files": {MaxFiles: 1}, "time": {MaxDuration: time.Nanosecond}} {
		r := mustOK(t, call(newFT(ws, Options{Symbols: opts}).findReferencesTool(), `{"name":"calc_total"}`))
		if res := r.Data["result"].(ReferenceResult); !res.Partial || r.Data["partial"] != true || len(res.Sites) >= 4 ||
			!strings.HasSuffix(r.Text, "\n"+SymbolPartialMarker(reason)) || !strings.Contains(r.Text, " [partial]\n") {
			t.Errorf("%s: partial %v, %d sites\n%s", reason, res.Partial, len(res.Sites), r.Text)
		}
	}
	if res := refResult(t, newFT(ws, Options{}), `{"name":"calc_total"}`); res.Partial || len(res.Sites) != 4 {
		t.Errorf("unbounded: partial %v, %v", res.Partial, siteList(res))
	}
}
