package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// event is one call or statement the order check records, in execution
// order: main's body with the bodies of the package functions it calls
// inlined where they are called.
type event struct {
	kind string // parseFlags, version, confine, redirectStdout, agent.New, go
	at   string // file:line
}

// startupEvents walks main and the package-local functions it calls,
// recording the events in the order they run.
func startupEvents(fset *token.FileSet, files []*ast.File) ([]event, error) {
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				funcs[fd.Name.Name] = fd
			}
		}
	}
	main := funcs["main"]
	if main == nil {
		return nil, fmt.Errorf("no func main")
	}
	var events []event
	pos := func(n ast.Node) string {
		p := fset.Position(n.Pos())
		return fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line)
	}
	visiting := map[string]bool{}
	var walk func(n ast.Node, inlined bool)
	walk = func(n ast.Node, inlined bool) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.GoStmt:
				events = append(events, event{"go", pos(x)})
			case *ast.IfStmt:
				if sel, ok := x.Cond.(*ast.SelectorExpr); ok && sel.Sel.Name == "version" {
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if r, ok := m.(*ast.ReturnStmt); ok {
							events = append(events, event{"version", pos(r)})
						}
						return true
					})
					return false
				}
			case *ast.CallExpr:
				// Arguments run before the call itself.
				for _, a := range x.Args {
					walk(a, inlined)
				}
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					switch fn.Name {
					case "parseFlags", "redirectStdout":
						events = append(events, event{fn.Name, pos(x)})
					default:
						if fd := funcs[fn.Name]; fd != nil && !visiting[fn.Name] && inlined {
							visiting[fn.Name] = true
							walk(fd.Body, true)
							visiting[fn.Name] = false
						}
					}
				case *ast.SelectorExpr:
					if id, ok := fn.X.(*ast.Ident); ok {
						switch {
						case id.Name == "confine" && fn.Sel.Name == "Setup":
							events = append(events, event{"confine", pos(x)})
						case id.Name == "agent" && fn.Sel.Name == "New":
							events = append(events, event{"agent.New", pos(x)})
						}
					}
				}
				return false
			}
			return true
		})
	}
	walk(main.Body, true)
	return events, nil
}

// checkOrder fails, naming the file and line, unless parseFlags precedes the
// --version return, which precedes the one confinement call, which precedes
// every redirectStdout, agent.New and go statement.
func checkOrder(events []event) error {
	idx := func(kind string) []int {
		var out []int
		for i, e := range events {
			if e.kind == kind {
				out = append(out, i)
			}
		}
		return out
	}
	conf := idx("confine")
	if len(conf) != 1 {
		return fmt.Errorf("want exactly one confine.Setup call, found %d", len(conf))
	}
	c := conf[0]
	for _, k := range []string{"parseFlags", "version"} {
		is := idx(k)
		if len(is) == 0 {
			return fmt.Errorf("no %s before the confinement call", k)
		}
		for _, i := range is {
			if i > c {
				return fmt.Errorf("%s at %s comes after confine.Setup at %s", k, events[i].at, events[c].at)
			}
		}
	}
	if p, v := idx("parseFlags")[0], idx("version")[0]; p > v {
		return fmt.Errorf("the --version return at %s precedes parseFlags at %s", events[v].at, events[p].at)
	}
	for i, e := range events {
		if (e.kind == "redirectStdout" || e.kind == "agent.New" || e.kind == "go") && i < c {
			return fmt.Errorf("%s at %s precedes confine.Setup at %s", e.kind, e.at, events[c].at)
		}
	}
	return nil
}

func parseDir(t *testing.T, fset *token.FileSet, files map[string]string) []*ast.File {
	t.Helper()
	var out []*ast.File
	for name, src := range files {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// TS-15-18: an AST test pins main's order: flags, then --version, then
// confinement, then redirectStdout, agent.New, goroutines and serving.
// 15-REQ-3.4, 15-REQ-3.3.
func TestStartupOrder_TS15_18(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	real := map[string]string{}
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			b, err := os.ReadFile(n)
			if err != nil {
				t.Fatal(err)
			}
			real[n] = string(b)
		}
	}
	fset := token.NewFileSet()
	events, err := startupEvents(fset, parseDir(t, fset, real))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOrder(events); err != nil {
		t.Errorf("the real source: %v (events %v)", err, events)
	}

	good := `package main
func main() { run() }
func run() int {
	cfg, _ := parseFlags(nil)
	if cfg.version {
		return 0
	}
	info, _ := confine.Setup(confine.Inputs{})
	redirectStdout()
	agent.New(agent.Config{Landlock: info})
	go serve()
	return 0
}
`
	fset = token.NewFileSet()
	ev, err := startupEvents(fset, parseDir(t, fset, map[string]string{"good.go": good}))
	if err != nil || checkOrder(ev) != nil {
		t.Fatalf("the good synthetic source fails: %v %v", err, checkOrder(ev))
	}
	for name, bad := range map[string]struct{ from, to, line string }{
		"agentnew.go": {"\tinfo, _ := confine.Setup", "\tagent.New(agent.Config{})\n\tinfo, _ := confine.Setup", "agentnew.go:8"},
		"redirect.go": {"\tinfo, _ := confine.Setup", "\tredirectStdout()\n\tinfo, _ := confine.Setup", "redirect.go:8"},
		"gostmt.go":   {"\tinfo, _ := confine.Setup", "\tgo serve()\n\tinfo, _ := confine.Setup", "gostmt.go:8"},
		"version.go":  {"\tif cfg.version {\n\t\treturn 0\n\t}\n\tinfo, _ := confine.Setup(confine.Inputs{})", "\tinfo, _ := confine.Setup(confine.Inputs{})\n\tif cfg.version {\n\t\treturn 0\n\t}", "version.go:7"},
	} {
		src := strings.Replace(good, bad.from, bad.to, 1)
		fset := token.NewFileSet()
		ev, err := startupEvents(fset, parseDir(t, fset, map[string]string{name: src}))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkOrder(ev); err == nil || !strings.Contains(err.Error(), bad.line) {
			t.Errorf("%s: %v, want a failure naming %s", name, err, bad.line)
		}
	}
}
