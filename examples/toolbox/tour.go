package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// sample is the project the tour works in: a small Go module, so that the
// navigation tools have declarations to find and a caller to point at.
var sample = map[string]string{
	"go.mod": "module example.com/greeter\n\ngo 1.22\n",
	"main.go": `package main

import (
	"fmt"

	"example.com/greeter/greeter"
)

func main() {
	g := greeter.New("")
	fmt.Println(g.Greet())
}
`,
	"greeter/greeter.go": `// Package greeter says hello.
package greeter

import "fmt"

// DefaultName is who Greet greets when New is given no name.
const DefaultName = "world"

// Greeter says hello to someone.
type Greeter struct {
	Name string
}

// New returns a Greeter for name, or for DefaultName when name is empty.
func New(name string) *Greeter {
	if name == "" {
		name = DefaultName
	}
	return &Greeter{Name: name}
}

// Greet returns the greeting.
func (g *Greeter) Greet() string {
	return fmt.Sprintf("hello, %s", g.Name)
}
`,
	"greeter/greeter_test.go": `package greeter

import "testing"

func TestGreet(t *testing.T) {
	if got := New("Ada").Greet(); got != "hello, Ada" {
		t.Fatalf("Greet() = %q", got)
	}
}
`,
	"notes/todo.md": "# TODO\n\n- greet in more languages\n- let Greet take a name\n",
}

// step is one call of the tour: what it shows, the tool and the JSON input.
type step struct {
	about string
	tool  string
	input string
}

// steps are the calls of the tour, in the order that reads best: look around,
// read, search, navigate, change, run a program, and then get things wrong.
// Each input is exactly what a model would send, so it can be copied into
// `call` or `repl`.
var steps = []step{
	{"list_files: one directory, ignore rules applied", "list_files", `{}`},
	{"find_files: files by glob; ** crosses directories", "find_files", `{"pattern":"**/*.go"}`},
	{"read_file: a window of a file; offset is 1-based", "read_file", `{"path":"greeter/greeter.go","offset":13,"limit":9}`},
	{"search_files: a regular expression, grep-shaped, with context", "search_files", `{"pattern":"Default\\w+","file_glob":"**/*.go","context_lines":1}`},
	{"file_outline: the declarations of one file, without its bodies", "file_outline", `{"path":"greeter/greeter.go"}`},
	{"find_symbol: declarations by name across the workspace, best match first", "find_symbol", `{"name":"Greet"}`},
	{"find_symbol: narrowed to an exact function name", "find_symbol", `{"name":"New","kind":"func","exact":true}`},
	{"find_references: the callers of a method; resolved means the type checker agrees", "find_references", `{"name":"Greeter.Greet"}`},
	{"find_references: the same, without the tests", "find_references", `{"name":"Greeter.Greet","include_tests":false}`},
	{"write_file: creates the file and its directories", "write_file", `{"path":"notes/hello.txt","content":"hello from write_file\n"}`},
	{"edit_file: replaces text that occurs exactly once", "edit_file", `{"path":"notes/todo.md","edits":[{"old_string":"- greet in more languages","new_string":"- [x] greet in more languages"}]}`},
	{"edit_file: a text that occurs twice is rejected, never replaced everywhere", "edit_file", `{"path":"notes/todo.md","edits":[{"old_string":"- ","new_string":"* "}]}`},
	{"execute: a shell line; the guard allows echo", "execute", `{"command":"echo hello from execute"}`},
	{"execute: the guard allows no rm", "execute", `{"command":"rm -rf notes"}`},
	{"run_command: an argument vector, no shell, so ; is just a character", "run_command", `{"argv":["echo","a;b"]}`},
	{"an error is a result: a path outside the workspace", "read_file", `{"path":"../outside.txt"}`},
	{"an error is a result: arguments the schema refuses", "find_symbol", `{}`},
	{"an error is a result: a tool that does not exist", "no_such_tool", `{}`},
}

// tourLines bounds what the tour prints of one result; call prints it whole.
const tourLines = 14

// tour runs steps in a sample project that exists for the run, then shows the
// two other ways to get at the same answers.
func tour(ctx context.Context, w io.Writer) error {
	root, err := os.MkdirTemp("", "agentkit-toolbox-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	for rel, content := range sample {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			return err
		}
	}
	// NoGlobalExcludes keeps the tour the same on every machine: the
	// developer's own global gitignore does not hide a file from it.
	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		return err
	}
	// Only echo may run, so the rm above is blocked and the echo is not.
	runner, err := newRunner(ws, []string{"echo"})
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "A sample project with %d files, and %d tool calls through a Runner.\n", len(sample), len(steps))
	for _, s := range steps {
		fmt.Fprintf(w, "\n## %s\n$ %s %s\n", s.about, s.tool, s.input)
		show(w, runner.Call(ctx, s.tool, []byte(s.input)), tourLines, false)
	}
	return typed(ctx, w, ws)
}

// typed shows what the tool envelope hides: the same engines return typed
// values, and a tool result keeps them in Data.
func typed(ctx context.Context, w io.Writer, ws *workspace.Workspace) error {
	fmt.Fprintln(w, "\n## Without the envelope: tools.Search and tools.References return typed values")

	// tools.Search is search_files without the tool: SearchParams in,
	// SearchResult out, and none of the tool's rendering.
	found, err := tools.Search(ctx, ws.Root(), tools.SearchParams{Pattern: "Greet", FileGlob: "**/*.go"})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "tools.Search: %d matches in %d files searched; first %s:%d\n",
		len(found.Matches), found.FilesSearched, found.Matches[0].File, found.Matches[0].Line)

	// find_symbol keeps its matches in Data as []tools.SymbolMatch, and a match
	// converts to the outline.Decl that tools.References takes as its target.
	runner, err := newRunner(ws, nil)
	if err != nil {
		return err
	}
	res := runner.Call(ctx, "find_symbol", []byte(`{"name":"Greet","kind":"method","exact":true}`))
	matches, _ := res.Data["symbols"].([]tools.SymbolMatch)
	if !res.OK || len(matches) == 0 {
		return fmt.Errorf("find_symbol found no method Greet: %s %s", res.Error, res.Detail)
	}
	fmt.Fprintf(w, "find_symbol: Data[\"symbols\"][0] is %s %s, lines %d-%d of %s\n",
		matches[0].Kind, matches[0].Name, matches[0].StartLine, matches[0].EndLine, matches[0].Path)
	refs, err := tools.References(ctx, ws, matches[0].Decl(), tools.ReferenceOptions{IncludeTests: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "tools.References: %d sites by the %s backend\n", len(refs.Sites), refs.Backend)
	for _, s := range refs.Sites {
		fmt.Fprintf(w, "  %s:%d:%d %s, inside %s\n", s.Path, s.Line, s.Column, s.Confidence, s.Enclosing.Name)
	}

	// The same typed value is what the find_references tool reports in
	// Data["result"].
	again := runner.Call(ctx, "find_references", []byte(`{"name":"Greeter.Greet"}`))
	if r, ok := again.Data["result"].(tools.ReferenceResult); ok {
		fmt.Fprintf(w, "find_references: Data[\"result\"] is a tools.ReferenceResult with %d sites\n", len(r.Sites))
	}
	fmt.Fprintln(w, "\nToolbox example completed successfully")
	return nil
}
