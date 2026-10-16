package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"hash/fnv"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// writeTree writes files (path -> content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// hermeticEnv points the real ignore environment at nothing, so a test that
// reads it does not depend on whose machine runs it.
func hermeticEnv(t *testing.T) {
	t.Helper()
	cfg := t.TempDir()
	empty := filepath.Join(cfg, "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfg, "xdg"))
	t.Setenv("HOME", filepath.Join(cfg, "home"))
}

// callTool runs one call of the named tool and fails the test on an error
// result.
func callTool(t *testing.T, ts []core.Tool, name, args string) core.ToolResult {
	t.Helper()
	res := toolIn(t, ts, name).Execute(context.Background(), json.RawMessage(args))
	if !res.OK {
		t.Fatalf("%s %s: %s: %s", name, args, res.Error, res.Detail)
	}
	return res
}

// candidateIndex is an index that also answers CandidateFiles, with a fixed
// answer: the way an embedder's index sparing find_references a walk does.
type candidateIndex struct {
	stubIndex
	files []string
}

func (c candidateIndex) CandidateFiles(context.Context, string) ([]string, error) {
	return c.files, nil
}

// TS-20-37: tools.Options has the five fields, the default spill directory is
// derived from a hash of the root, and the tool constructors stay unexported.
func TestOptionsSpillDirectoryAndConstructors_TS20_37(t *testing.T) {
	typ := reflect.TypeFor[tools.Options]()
	var fields []string
	for i := 0; i < typ.NumField(); i++ {
		fields = append(fields, typ.Field(i).Name)
	}
	slices.Sort(fields)
	if want := []string{"DisableSpill", "Env", "Index", "SpillDir", "Symbols"}; !slices.Equal(fields, want) {
		t.Errorf("tools.Options has the fields %v, want %v", fields, want)
	}

	// The default spill directory shows in the marker of a result that spills.
	spillDir := func(root string) string {
		t.Helper()
		ws := openForDir(t, root)
		ts, err := tools.For(ws, tools.Options{})
		if err != nil {
			t.Fatal(err)
		}
		res := callTool(t, ts, "execute", `{"command":"head -c 200000 /dev/zero | tr '\\0' x"}`)
		m := regexp.MustCompile(`agentkit-spill-[0-9a-f]{8}`).FindString(res.Text + res.Detail + fmt.Sprint(res.Data))
		if m == "" {
			t.Fatalf("a result of 200000 bytes did not spill: %.200q", res.Text)
		}
		return m
	}
	a, b := t.TempDir(), t.TempDir()
	first, again, other := spillDir(a), spillDir(a), spillDir(b)
	if first != again {
		t.Errorf("the same root gave the spill directories %s and %s", first, again)
	}
	if first == other {
		t.Errorf("two roots gave the same spill directory %s", first)
	}
	h := fnv.New32a()
	root := openForDir(t, a).Root()
	_, _ = h.Write([]byte(root))
	if want := fmt.Sprintf("agentkit-spill-%08x", h.Sum32()); first != want {
		t.Errorf("the spill directory for %s is %s, want %s (a hash of the root)", root, first, want)
	}

	// No exported identifier of tools is an individual tool constructor.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		pf, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pf.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.Results == nil || fd.Name.Name == "For" {
				continue
			}
			for _, r := range fd.Type.Results.List {
				if sel, ok := r.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Tool" {
					t.Errorf("%s: %s returns a core.Tool: an individual tool constructor must stay unexported", f, fd.Name.Name)
				}
			}
		}
	}
}

// A Go module for the walks to cover: ok.go and a/c.go reference Target; a/b.go
// is ignored by a/.gitignore and secret_a.go by the workspace's global
// excludes file (secret_*).
var walkFixture = map[string]string{
	"go.mod":       "module example.com/m\n\ngo 1.27\n",
	"ok.go":        "package m\n\nfunc Target() int { return 1 }\n\nfunc UseOK() int { return Target() }\n\n// needleSym\n",
	"secret_a.go":  "package m\n\nfunc SecretDecl() int { return Target() }\n\n// needleSym\n",
	"a/.gitignore": "b.go\n",
	"a/b.go":       "package a\n\nimport \"example.com/m\"\n\nfunc HiddenDecl() int { return m.Target() }\n\n// needleSym\n",
	"a/c.go":       "package a\n\nimport \"example.com/m\"\n\nfunc UseC() int { return m.Target() }\n\n// needleSym\n",
}

// workspaceIgnoring opens a workspace over dir whose global excludes file,
// found through the GitConfig hook, ignores secret_*; lookups counts the
// hook's calls.
func workspaceIgnoring(t *testing.T, dir string, lookups *atomic.Int32) *workspace.Workspace {
	t.Helper()
	global := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(global, []byte("secret_*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ig := workspace.NoGlobalExcludes()
	ig.GitConfig = func() string {
		if lookups != nil {
			lookups.Add(1)
		}
		return global
	}
	ws, err := workspace.Open(dir, workspace.Options{Ignore: ig})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func assertNeither(t *testing.T, what, got string) {
	t.Helper()
	for _, bad := range []string{"secret_a.go", "a/b.go"} {
		if strings.Contains(got, bad) {
			t.Errorf("%s returned %q, which the workspace's rules exclude:\n%s", what, bad, got)
		}
	}
}

// TS-20-39: every tool walk uses the workspace's ignore rules, the
// non-default ones included: find_files, search_files, find_symbol (a build and
// a refresh), find_references (the Go package load and, with an index, the
// candidate filter) and References.
func TestEveryToolWalkUsesTheWorkspaceRules_TS20_39(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, walkFixture)
	ws := workspaceIgnoring(t, dir, nil)
	text := func(res core.ToolResult) string { return res.Text + fmt.Sprint(res.Data) }

	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ff := text(callTool(t, ts, "find_files", `{"pattern":"**/*.go"}`))
	assertNeither(t, "find_files", ff)
	if !strings.Contains(ff, "ok.go") || !strings.Contains(ff, "a/c.go") {
		t.Errorf("find_files lost the files the rules keep: %s", ff)
	}
	sf := text(callTool(t, ts, "search_files", `{"pattern":"Target"}`))
	assertNeither(t, "search_files", sf)
	if !strings.Contains(sf, "ok.go") || !strings.Contains(sf, "a/c.go") {
		t.Errorf("search_files lost the files the rules keep: %s", sf)
	}

	// find_symbol builds its table, then refreshes it after a write.
	for _, name := range []string{"SecretDecl", "HiddenDecl"} {
		assertNeither(t, "find_symbol "+name, text(callTool(t, ts, "find_symbol", `{"name":"`+name+`"}`)))
	}
	callTool(t, ts, "write_file", `{"path":"secret_late.go","content":"package m\n\nfunc SecretLate() {}\n"}`)
	callTool(t, ts, "write_file", `{"path":"late.go","content":"package m\n\nfunc LateDecl() {}\n"}`)
	if got := text(callTool(t, ts, "find_symbol", `{"name":"SecretLate"}`)); strings.Contains(got, "secret_late.go") {
		t.Errorf("find_symbol found a declaration in a file the global excludes name, after a refresh: %s", got)
	}
	if got := text(callTool(t, ts, "find_symbol", `{"name":"LateDecl"}`)); !strings.Contains(got, "late.go") {
		t.Errorf("find_symbol did not find a new file's declaration after a refresh: %s", got)
	}

	// find_references: the Go package load.
	fr := text(callTool(t, ts, "find_references", `{"name":"Target"}`))
	assertNeither(t, "find_references", fr)
	if !strings.Contains(fr, "ok.go") || !strings.Contains(fr, "a/c.go") {
		t.Errorf("find_references lost the sites the rules keep: %s", fr)
	}

	// tools.References: the same pass without a tool.
	res, err := tools.References(context.Background(), ws, outline.Decl{Kind: outline.KindFunc, Name: "Target"}, tools.ReferenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, s := range res.Sites {
		paths = append(paths, s.Path)
	}
	slices.Sort(paths)
	if want := []string{"a/c.go", "ok.go"}; !slices.Equal(paths, want) {
		t.Errorf("References found sites in %v, want %v", paths, want)
	}

	// An index's candidate files are filtered by the same rules, nested
	// .gitignore files included.
	idx := candidateIndex{stubIndex{"idx_search"}, []string{"secret_a.go", "a/b.go", "a/c.go", "ok.go"}}
	ts, err = tools.For(ws, tools.Options{Index: idx})
	if err != nil {
		t.Fatal(err)
	}
	got := text(callTool(t, ts, "find_references", `{"name":"needleSym"}`))
	assertNeither(t, "find_references over an index's candidates", got)
	if !strings.Contains(got, "ok.go") || !strings.Contains(got, "a/c.go") {
		t.Errorf("the candidate filter dropped files the rules keep: %s", got)
	}
}

// TS-20-40: a tool set built from one workspace runs at most one
// core.excludesFile lookup across all of its walks.
func TestOneLookupAcrossAToolSetsWalks_TS20_40(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, walkFixture)
	var lookups atomic.Int32
	ws := workspaceIgnoring(t, dir, &lookups)
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := lookups.Load(); n != 0 {
		t.Fatalf("building the tool set ran the lookup %d times, want 0", n)
	}

	for i := 0; i < 2; i++ {
		callTool(t, ts, "find_files", `{"pattern":"**/*.go"}`)
		callTool(t, ts, "search_files", `{"pattern":"Target"}`)
		callTool(t, ts, "find_symbol", `{"name":"UseOK"}`)
		callTool(t, ts, "find_references", `{"name":"Target"}`)
		callTool(t, ts, "list_files", `{}`)
		// A write marks the table and the reference cache for a refresh.
		callTool(t, ts, "write_file", fmt.Sprintf(`{"path":"extra%d.go","content":"package m\n\nfunc Extra%d() {}\n"}`, i, i))
	}
	if n := lookups.Load(); n != 1 {
		t.Errorf("the tool set ran the core.excludesFile lookup %d times across its walks, want exactly 1", n)
	}
}

// TS-20-46: ReferenceOptions, ReferenceSite and ReferenceResult keep their
// fields, as the exported-API golden recorded them before the change.
func TestReferenceTypesKeepTheirFields_TS20_46(t *testing.T) {
	fields := func(typ reflect.Type) []string {
		var out []string
		for i := 0; i < typ.NumField(); i++ {
			if f := typ.Field(i); f.IsExported() {
				out = append(out, f.Name+" "+f.Type.String())
			}
		}
		return out
	}
	for typ, want := range map[reflect.Type][]string{
		reflect.TypeFor[tools.ReferenceOptions](): {"Path string", "IncludeTests bool", "MaxResults int"},
		reflect.TypeFor[tools.ReferenceSite](): {"Path string", "Line int", "Column int", "Confidence string",
			"Enclosing outline.Decl", "Source string"},
		reflect.TypeFor[tools.ReferenceResult](): {"Target outline.Decl", "Sites []tools.ReferenceSite", "Backend string",
			"Partial bool", "PackagesChecked int", "Errors int", "Truncated bool"},
	} {
		if got := fields(typ); !slices.Equal(got, want) {
			t.Errorf("%s has the fields %v, want %v", typ.Name(), got, want)
		}
	}
}

// TS-20-47: tools declares none of the removed identifiers, as a type, alias,
// var, const, func or field; References exists as a plain function.
func TestToolsDeclaresNoneOfTheRemovedIdentifiers_TS20_47(t *testing.T) {
	removed := map[string]bool{}
	for _, n := range []string{"Workspace", "NewWorkspace", "Walk", "WalkOptions", "IgnoreOptions", "NoGlobalExcludes",
		"Normalize", "ErrPathNotAllowed", "ErrPathMalformed", "ErrNoHome", "All"} {
		removed[n] = true
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	referencesFunc := false
	for _, f := range files {
		pf, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pf.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && removed[d.Name.Name] {
					t.Errorf("%s declares the func %s", f, d.Name.Name)
				}
				if d.Recv == nil && d.Name.Name == "References" {
					referencesFunc = true
				}
				if d.Recv != nil && d.Name.Name == "References" && strings.Contains(fmt.Sprint(d.Recv.List[0].Type), "Workspace") {
					t.Errorf("%s declares a References method on a Workspace", f)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if removed[s.Name.Name] {
							t.Errorf("%s declares the type %s", f, s.Name.Name)
						}
						if st, ok := s.Type.(*ast.StructType); ok && s.Name.Name == "Options" {
							for _, fl := range st.Fields.List {
								for _, n := range fl.Names {
									if n.Name == "Workspace" || n.Name == "Ignore" {
										t.Errorf("%s: Options declares the field %s", f, n.Name)
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if removed[n.Name] {
								t.Errorf("%s declares %s", f, n.Name)
							}
						}
					}
				}
			}
		}
	}
	if !referencesFunc {
		t.Error("tools declares no plain function References")
	}
}
