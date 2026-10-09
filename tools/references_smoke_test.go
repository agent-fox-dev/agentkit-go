package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// smokeWorkspace writes files under a fresh workspace root.
func smokeWorkspace(t *testing.T, files map[string]string) *tools.Workspace {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// smokeTool returns the named tool from the real default tool set.
func smokeTool(t *testing.T, ws *tools.Workspace, name string) core.Tool {
	t.Helper()
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tools.All has no %s", name)
	return core.Tool{}
}

// smokeRefs runs find_references and returns its result and structured data.
func smokeRefs(t *testing.T, fr core.Tool, args string) (core.ToolResult, tools.ReferenceResult) {
	t.Helper()
	res := fr.Execute(context.Background(), json.RawMessage(args))
	if !res.OK {
		t.Fatalf("find_references %s: OK false: %s", args, res.Text)
	}
	data, ok := res.Data["result"].(tools.ReferenceResult)
	if !ok {
		t.Fatalf("Data[result] is %T", res.Data["result"])
	}
	return res, data
}

// siteAt returns the site at path and line.
func siteAt(sites []tools.ReferenceSite, path string, line int) (tools.ReferenceSite, bool) {
	for _, s := range sites {
		if s.Path == path && s.Line == line {
			return s, true
		}
	}
	return tools.ReferenceSite{}, false
}

// pythonOutlined reports whether this build outlines Python (tree-sitter, cgo).
func pythonOutlined(t *testing.T, ws *tools.Workspace, rel string) bool {
	t.Helper()
	f, err := outline.Outline(context.Background(), filepath.Join(ws.Root, rel), nil, outline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return f.Backend != outline.BackendNone
}

var goRunnerFixture = map[string]string{
	"go.mod": "module example.com/app\n\ngo 1.22\n",
	"runner/runner.go": `package runner

import "fmt"

// Runner runs a job.
type Runner struct{ Name string }

// Run runs it.
func (r *Runner) Run() error {
	fmt.Println(r.Name)
	return nil
}

// Job is anything that runs.
type Job interface{ Run() error }
`,
	"other/other.go": `package other

// Other has an unrelated Run.
type Other struct{}

func (Other) Run() error { return nil }
`,
	"cmd/main.go": `package main

import (
	rn "example.com/app/runner"
	"example.com/app/other"
)

func start() error {
	r := &rn.Runner{Name: "x"}
	return r.Run()
}

func unrelated() error {
	return other.Other{}.Run()
}

func main() { _ = start(); _ = unrelated() }
`,
	"runner/runner_test.go": `package runner

import "testing"

func TestRun(t *testing.T) {
	r := &Runner{}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
}
`,
}

// TS-05-55 (smoke): End-to-end Go method caller resolution across packages
// with exact type analysis.
// Verifies: 05-PATH-1, 05-REQ-3.3
func TestReferencesSmoke_GoMethodCallers_TS05_55(t *testing.T) {
	ws := smokeWorkspace(t, goRunnerFixture)
	fr := smokeTool(t, ws, "find_references")

	res, data := smokeRefs(t, fr, `{"name":"Runner.Run"}`)

	if data.Target.Name != "Run" || data.Target.Container != "Runner" || data.Target.StartLine != 9 {
		t.Fatalf("Target = %+v, want Runner.Run at runner/runner.go:9", data.Target)
	}
	if data.Backend != "go/types" {
		t.Fatalf("Backend = %q, want go/types", data.Backend)
	}
	call, ok := siteAt(data.Sites, "cmd/main.go", 10)
	if !ok {
		t.Fatalf("no site for the call through the renamed import at cmd/main.go:10: %+v", data.Sites)
	}
	if call.Confidence != "resolved" || call.Enclosing.Name != "start" || call.Enclosing.Kind != outline.KindFunc {
		t.Fatalf("cmd/main.go:10 = %+v, want resolved and enclosed by func start", call)
	}
	if _, ok := siteAt(data.Sites, "cmd/main.go", 14); ok {
		t.Fatalf("the unrelated Other.Run call at cmd/main.go:14 was reported: %+v", data.Sites)
	}
	testCall, ok := siteAt(data.Sites, "runner/runner_test.go", 7)
	if !ok || testCall.Confidence != "resolved" || testCall.Enclosing.Name != "TestRun" {
		t.Fatalf("runner/runner_test.go:7 = %+v (found %v), want resolved in TestRun", testCall, ok)
	}
	if !strings.HasPrefix(res.Text, "find_references Runner.Run  (go/types, ") {
		t.Fatalf("header: %q", strings.SplitN(res.Text, "\n", 2)[0])
	}
}

// TS-05-56 (smoke): End-to-end lexical reference discovery in Python files
// attributed to enclosing functions.
// Verifies: 05-PATH-2, 05-REQ-4.3
func TestReferencesSmoke_PythonLexical_TS05_56(t *testing.T) {
	ws := smokeWorkspace(t, map[string]string{
		"billing/tax.py": "def calculate_tax(amount):\n    return amount * 0.2\n",
		"billing/shop.py": "# calculate_tax is applied at checkout\n" +
			"def checkout(total):\n" +
			"    label = \"calculate_tax\"\n" +
			"    return calculate_tax(total)\n",
	})
	fr := smokeTool(t, ws, "find_references")

	_, data := smokeRefs(t, fr, `{"name":"calculate_tax"}`)

	if !pythonOutlined(t, ws, "billing/shop.py") {
		// Without cgo there is no Python outline: no declaration is found
		// and every hit is text (docs/errata/05_find_references.md).
		if data.Backend != "text" {
			t.Fatalf("Backend = %q without a Python outline, want text", data.Backend)
		}
		for _, s := range data.Sites {
			if s.Confidence != "text" {
				t.Fatalf("site %+v without a Python outline, want text", s)
			}
		}
		return
	}

	if data.Target.Name != "calculate_tax" || data.Target.StartLine != 1 {
		t.Fatalf("Target = %+v, want calculate_tax at billing/tax.py:1", data.Target)
	}
	if data.Backend != "lexical" {
		t.Fatalf("Backend = %q, want lexical", data.Backend)
	}
	call, ok := siteAt(data.Sites, "billing/shop.py", 4)
	if !ok || call.Confidence != "lexical" || call.Enclosing.Name != "checkout" || call.Enclosing.StartLine != 2 {
		t.Fatalf("billing/shop.py:4 = %+v (found %v), want lexical, enclosed by checkout at line 2", call, ok)
	}
	for _, line := range []int{1, 3} {
		s, ok := siteAt(data.Sites, "billing/shop.py", line)
		if !ok || s.Confidence != "text" {
			t.Fatalf("billing/shop.py:%d (comment or string) = %+v (found %v), want text", line, s, ok)
		}
	}
	if s, ok := siteAt(data.Sites, "billing/shop.py", 1); ok && s.Enclosing.Kind != "file" {
		t.Fatalf("top-level comment at billing/shop.py:1 enclosed by %+v, want <file>", s.Enclosing)
	}
}

// TS-05-57 (smoke): End-to-end fallback text search for an undeclared
// identifier across workspace files.
// Verifies: 05-PATH-3, 05-REQ-2.4, 05-REQ-4.5
func TestReferencesSmoke_UndeclaredText_TS05_57(t *testing.T) {
	ws := smokeWorkspace(t, map[string]string{
		"config/settings.py": "ENABLED = NONEXISTENT_FLAG or False\n",
		"docs/notes.md":      "Set NONEXISTENT_FLAG to turn it on.\n",
		"main.go":            "package main\n\nfunc main() { _ = \"NONEXISTENT_FLAG\" }\n",
	})
	fr := smokeTool(t, ws, "find_references")

	res, data := smokeRefs(t, fr, `{"name":"NONEXISTENT_FLAG"}`)

	if data.Target != (outline.Decl{}) {
		t.Fatalf("Target = %+v, want the zero outline.Decl", data.Target)
	}
	if res.Data["target"] != (outline.Decl{}) {
		t.Fatalf("Data[target] = %+v, want the zero outline.Decl", res.Data["target"])
	}
	if data.Backend != "text" {
		t.Fatalf("Backend = %q, want text", data.Backend)
	}
	header := strings.SplitN(res.Text, "\n", 2)[0]
	if !strings.HasPrefix(header, "find_references NONEXISTENT_FLAG  (text, 3 references in 3 files; 3 text matches)") ||
		!strings.Contains(header, "0 declarations matched") {
		t.Fatalf("header %q does not state 0 declarations matched", header)
	}
	if len(data.Sites) != 3 {
		t.Fatalf("sites = %+v, want one per file", data.Sites)
	}
	for _, s := range data.Sites {
		if s.Confidence != "text" {
			t.Fatalf("site %+v, want text", s)
		}
	}
}

// TS-05-58 (smoke): End-to-end programmatic caller analysis via the
// Workspace.References embedder seam.
// Verifies: 05-PATH-4, 05-REQ-9.2
func TestReferencesSmoke_WorkspaceReferences_TS05_58(t *testing.T) {
	ws := smokeWorkspace(t, goRunnerFixture)
	fr := smokeTool(t, ws, "find_references")
	_, viaTool := smokeRefs(t, fr, `{"name":"Runner.Run","include_tests":true}`)

	got, err := ws.References(context.Background(), viaTool.Target, tools.ReferenceOptions{IncludeTests: true})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	if got.Target != viaTool.Target || got.Backend != viaTool.Backend || !reflect.DeepEqual(got.Sites, viaTool.Sites) {
		t.Fatalf("Workspace.References differs from find_references:\nseam: %+v\ntool: %+v", got, viaTool)
	}
	if len(got.Sites) < 2 {
		t.Fatalf("sites = %+v, want the main.go and test callers", got.Sites)
	}
	rank := map[string]int{"resolved": 0, "lexical": 1, "text": 2}
	sorted := slices.IsSortedFunc(got.Sites, func(a, b tools.ReferenceSite) int {
		if d := rank[a.Confidence] - rank[b.Confidence]; d != 0 {
			return d
		}
		at, bt := strings.HasSuffix(a.Path, "_test.go"), strings.HasSuffix(b.Path, "_test.go")
		if at != bt {
			if at {
				return 1
			}
			return -1
		}
		if c := strings.Compare(a.Path, b.Path); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return a.Column - b.Column
	})
	if !sorted {
		t.Fatalf("sites are not ranked by confidence, test status, path, line: %+v", got.Sites)
	}
	if got.Sites[len(got.Sites)-1].Path != "runner/runner_test.go" {
		t.Fatalf("the test caller is not ranked after non-test callers: %+v", got.Sites)
	}

	// The struct default leaves test files out.
	noTests, err := ws.References(context.Background(), viaTool.Target, tools.ReferenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range noTests.Sites {
		if strings.HasSuffix(s.Path, "_test.go") {
			t.Fatalf("IncludeTests false returned %+v", s)
		}
	}
}

// TS-05-59 (smoke): End-to-end cache invalidation and revalidation after
// file editing through the tool set.
// Verifies: 05-PATH-5, 05-REQ-8.2, 05-REQ-8.4
func TestReferencesSmoke_WriteFileRevalidates_TS05_59(t *testing.T) {
	ws := smokeWorkspace(t, goRunnerFixture)
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	var fr, wf core.Tool
	for _, tl := range all {
		switch tl.Name {
		case "find_references":
			fr = tl
		case "write_file":
			wf = tl
		}
	}

	_, before := smokeRefs(t, fr, `{"name":"Runner.Run"}`)
	if _, ok := siteAt(before.Sites, "cmd/extra.go", 6); ok {
		t.Fatal("cmd/extra.go does not exist yet")
	}

	args, _ := json.Marshal(map[string]string{
		"path":    "cmd/extra.go",
		"content": "package main\n\nimport rn \"example.com/app/runner\"\n\nfunc again() error {\n\treturn (&rn.Runner{}).Run()\n}\n",
	})
	if res := wf.Execute(context.Background(), args); !res.OK {
		t.Fatalf("write_file: %s", res.Text)
	}

	_, after := smokeRefs(t, fr, `{"name":"Runner.Run"}`)
	s, ok := siteAt(after.Sites, "cmd/extra.go", 6)
	if !ok || s.Confidence != "resolved" || s.Enclosing.Name != "again" {
		t.Fatalf("cmd/extra.go:6 = %+v (found %v), want resolved in func again; sites %+v", s, ok, after.Sites)
	}
	if len(after.Sites) != len(before.Sites)+1 {
		t.Fatalf("sites before %d, after %d, want one more", len(before.Sites), len(after.Sites))
	}
}
