package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests read the sources of the examples, from this one: the three that
// call a model, which choose provider, model and effort the same way, and the
// five that stay on provider/faux and must not.

// modelExamples are the directories, from this one, of the examples that call a
// model. Each repeats its own few lines instead of sharing a helper.
var modelExamples = []string{"../codingagent", "../customtools", "."}

// offlineExamples are the examples scripted on provider/faux.
var offlineExamples = []string{"../agentdemo", "../codemode", "../sandbox", "../gateway", "../harness"}

// selectionVars are what the model-calling examples read.
var selectionVars = []string{"AGENTKIT_PROVIDER", "AGENTKIT_MODEL", "AGENTKIT_EFFORT"}

func parseFile(t *testing.T, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// importsOf are the import paths of f.
func importsOf(f *ast.File) []string {
	var out []string
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		out = append(out, p)
	}
	return out
}

// selectors are the "x.Sel" pairs of f, with x a plain identifier.
func selectors(f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				out = append(out, id.Name+"."+sel.Sel.Name)
			}
		}
		return true
	})
	return out
}

// stringLit is the value of a string literal, or "" and false.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// getenvOf is the name of the variable e reads with os.Getenv, if it is such a
// call.
func getenvOf(e ast.Expr) (string, bool) {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Getenv" {
		return "", false
	}
	if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "os" {
		return "", false
	}
	return stringLit(call.Args[0])
}

// TS-30-36 (30-REQ-8.1): each model-calling example reads AGENTKIT_PROVIDER,
// AGENTKIT_MODEL and AGENTKIT_EFFORT from the process environment in a function
// of its own, with the defaults empty, STANDARD and empty, passes them to
// agentkit.Config as Provider, Model and Effort with nothing in between, and
// imports no helper of the repository's examples or internals for it.
func TestExamplesReadTheThreeVariablesThemselves_TS30_36(t *testing.T) {
	for _, dir := range modelExamples {
		f := parseFile(t, filepath.Join(dir, "main.go"))
		sel := selectionFunc(t, dir, f)
		if sel == nil {
			continue
		}
		checkSelectionDefaults(t, dir, sel)
		checkSelectionReturns(t, dir, sel)
		checkConfigLiteral(t, dir, f)
		for _, imp := range importsOf(f) {
			if strings.Contains(imp, "/examples/") || strings.Contains(imp, "/internal/") {
				t.Errorf("%s imports %s: the examples share no helper", dir, imp)
			}
		}
	}
}

// selectionFunc is the function of f that reads AGENTKIT_MODEL with os.Getenv,
// which has to read all three variables and no other of AGENTKIT_.
func selectionFunc(t *testing.T, dir string, f *ast.File) *ast.FuncDecl {
	t.Helper()
	var sel *ast.FuncDecl
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var read []string
		ast.Inspect(fn, func(n ast.Node) bool {
			if name, ok := getenvOf(asExpr(n)); ok && strings.HasPrefix(name, "AGENTKIT_") {
				read = append(read, name)
			}
			return true
		})
		if slices.Contains(read, "AGENTKIT_MODEL") {
			sel = fn
			slices.Sort(read)
			if !slices.Equal(read, []string{"AGENTKIT_EFFORT", "AGENTKIT_MODEL", "AGENTKIT_PROVIDER"}) {
				t.Errorf("%s: %s reads %v, want exactly %v", dir, fn.Name.Name, read, selectionVars)
			}
		}
	}
	if sel == nil {
		t.Errorf("%s: no function reads AGENTKIT_MODEL with os.Getenv", dir)
	}
	return sel
}

// checkSelectionDefaults: the only string the function assigns is STANDARD, the
// model's default, so the other two default to nothing.
func checkSelectionDefaults(t *testing.T, dir string, sel *ast.FuncDecl) {
	t.Helper()
	var assigned []string
	ast.Inspect(sel, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok {
			for _, rhs := range as.Rhs {
				if s, ok := stringLit(rhs); ok {
					assigned = append(assigned, s)
				}
			}
		}
		return true
	})
	if !slices.Equal(assigned, []string{"STANDARD"}) {
		t.Errorf("%s: %s assigns the strings %q, want only the default \"STANDARD\"", dir, sel.Name.Name, assigned)
	}
}

// checkSelectionReturns: the function returns the provider and the effort as they
// are read, and the model as the variable it was read into.
func checkSelectionReturns(t *testing.T, dir string, sel *ast.FuncDecl) {
	t.Helper()
	var returned *ast.ReturnStmt
	ast.Inspect(sel, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok {
			returned = r
		}
		return true
	})
	if returned == nil || len(returned.Results) != 3 {
		t.Errorf("%s: %s does not return the three settings", dir, sel.Name.Name)
		return
	}
	if name, _ := getenvOf(returned.Results[0]); name != "AGENTKIT_PROVIDER" {
		t.Errorf("%s: the provider returned is not os.Getenv(\"AGENTKIT_PROVIDER\")", dir)
	}
	if _, ok := returned.Results[1].(*ast.Ident); !ok {
		t.Errorf("%s: the model returned is %T, want the variable read from AGENTKIT_MODEL", dir, returned.Results[1])
	}
	conv, ok := returned.Results[2].(*ast.CallExpr)
	if !ok || len(conv.Args) != 1 {
		t.Errorf("%s: the effort returned is not a conversion of one value", dir)
	} else if name, _ := getenvOf(conv.Args[0]); name != "AGENTKIT_EFFORT" {
		t.Errorf("%s: the effort returned is not os.Getenv(\"AGENTKIT_EFFORT\") converted to agentkit.Effort", dir)
	}
}

// checkConfigLiteral: the agentkit.Config literal takes the three settings as
// variables, untouched.
func checkConfigLiteral(t *testing.T, dir string, f *ast.File) {
	t.Helper()
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sx, ok := lit.Type.(*ast.SelectorExpr); !ok || sx.Sel.Name != "Config" {
			return true
		}
		found = true
		fields := map[string]ast.Expr{}
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if k, ok := kv.Key.(*ast.Ident); ok {
					fields[k.Name] = kv.Value
				}
			}
		}
		for _, field := range []string{"Provider", "Model", "Effort"} {
			if _, ok := fields[field].(*ast.Ident); !ok {
				t.Errorf("%s: agentkit.Config.%s is %T, want a plain variable holding what selection read", dir, field, fields[field])
			}
		}
		return true
	})
	if !found {
		t.Errorf("%s: no agentkit.Config literal", dir)
	}
}

// asExpr is n as an expression, or nil.
func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}

// TS-30-38 (30-REQ-8.2): none of the three imports the Anthropic SDK or calls
// catalog.Lookup or anthropic.Resolve.
func TestExamplesNeitherLookUpNorResolve_TS30_38(t *testing.T) {
	for _, dir := range modelExamples {
		f := parseFile(t, filepath.Join(dir, "main.go"))
		for _, imp := range importsOf(f) {
			if strings.Contains(imp, "anthropic-sdk-go") {
				t.Errorf("%s imports %s", dir, imp)
			}
		}
		for _, s := range selectors(f) {
			if s == "catalog.Lookup" || s == "anthropic.Resolve" {
				t.Errorf("%s calls %s", dir, s)
			}
		}
	}
}

// offlineProblems lists what is wrong with the example in dir for an example that
// stays on provider/faux: a mention of a selection variable in any of its Go
// files, an agent built without provider/faux, or no agent built at all.
func offlineProblems(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("%s: no Go files (%v)", dir, err)
	}
	var problems []string
	buildsAgent, usesFaux := false, false
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range selectionVars {
			if strings.Contains(string(raw), v) {
				problems = append(problems, file+" mentions "+v)
			}
		}
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f := parseFile(t, file)
		if slices.Contains(selectors(f), "agentkit.New") {
			buildsAgent = true
		}
		if slices.ContainsFunc(importsOf(f), func(p string) bool { return strings.HasSuffix(p, "/provider/faux") }) {
			usesFaux = true
		}
	}
	if buildsAgent && !usesFaux {
		problems = append(problems, dir+" builds an agent and imports no provider/faux")
	}
	if !buildsAgent {
		problems = append(problems, dir+" builds no agent: this test no longer looks at the example it names")
	}
	return problems
}

// TS-30-41 (30-REQ-8.6): the offline examples read none of the selection
// variables, and each builds its agent on provider/faux.
func TestOfflineExamplesStayOnFaux_TS30_41(t *testing.T) {
	for _, dir := range offlineExamples {
		for _, problem := range offlineProblems(t, dir) {
			t.Error(problem)
		}
	}
}

// The scan is what TS-30-41 stands on, so it must see each thing it looks for.
func TestOfflineScanSeesWhatItLooksFor_TS30_41(t *testing.T) {
	const good = "package main\n\nimport (\n\tagentkit \"github.com/agent-fox-dev/agentkit-go\"\n\t\"github.com/agent-fox-dev/agentkit-go/provider/faux\"\n)\n\nvar _ = faux.New\nvar _, _ = agentkit.New(agentkit.Config{})\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string // "" for no problem
	}{
		{"a faux example", map[string]string{"main.go": good}, ""},
		{"a variable in a comment", map[string]string{"main.go": good + "// AGENTKIT_MODEL\n"}, "AGENTKIT_MODEL"},
		{"a variable in a second file", map[string]string{"main.go": good, "more.go": "package main\nvar x = \"AGENTKIT_EFFORT\"\n"}, "AGENTKIT_EFFORT"},
		{"a variable in a test", map[string]string{"main.go": good, "main_test.go": "package main\n// AGENTKIT_PROVIDER\n"}, "AGENTKIT_PROVIDER"},
		{"an agent without faux", map[string]string{"main.go": "package main\n\nimport agentkit \"github.com/agent-fox-dev/agentkit-go\"\n\nvar _, _ = agentkit.New(agentkit.Config{})\n"}, "imports no provider/faux"},
		{"faux only in a test", map[string]string{
			"main.go":      "package main\n\nimport agentkit \"github.com/agent-fox-dev/agentkit-go\"\n\nvar _, _ = agentkit.New(agentkit.Config{})\n",
			"main_test.go": "package main\n\nimport _ \"github.com/agent-fox-dev/agentkit-go/provider/faux\"\n",
		}, "imports no provider/faux"},
		{"no agent", map[string]string{"main.go": "package main\n"}, "builds no agent"},
	} {
		dir := t.TempDir()
		for name, src := range c.files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got := strings.Join(offlineProblems(t, dir), "\n")
		switch {
		case c.want == "" && got != "":
			t.Errorf("%s: reported %q, want none", c.name, got)
		case c.want != "" && !strings.Contains(got, c.want):
			t.Errorf("%s: reported %q, want a problem mentioning %q", c.name, got, c.want)
		}
	}
}
