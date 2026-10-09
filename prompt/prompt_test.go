package prompt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// TS-02-55: The prompt builder emits the execute fallback only when no navigation tool is present
func TestExecuteFallbackSuppression_TS02_55(t *testing.T) {
	// Helper to build a minimal tool set.
	mkTool := func(name string, guidelines ...string) core.Tool {
		return core.Tool{Name: name, PromptGuidelines: guidelines}
	}

	tests := []struct {
		name         string
		tools        []core.Tool
		wantFallback bool
	}{
		{
			name:         "execute alone",
			tools:        []core.Tool{mkTool("execute")},
			wantFallback: true,
		},
		{
			name:         "execute with file_outline only",
			tools:        []core.Tool{mkTool("execute"), mkTool("file_outline")},
			wantFallback: false,
		},
		{
			name:         "execute with find_symbol only",
			tools:        []core.Tool{mkTool("execute"), mkTool("find_symbol")},
			wantFallback: false,
		},
		{
			name:         "execute with list_files",
			tools:        []core.Tool{mkTool("execute"), mkTool("list_files")},
			wantFallback: false,
		},
		{
			name:         "execute with all navigation tools",
			tools:        []core.Tool{mkTool("execute"), mkTool("list_files"), mkTool("find_files"), mkTool("search_files"), mkTool("file_outline"), mkTool("find_symbol")},
			wantFallback: false,
		},
		{
			name:         "no execute at all",
			tools:        []core.Tool{mkTool("read_file")},
			wantFallback: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Build("", tt.tools)
			hasFallback := contains(got, tools.ExecuteFallbackGuideline)
			if hasFallback != tt.wantFallback {
				t.Errorf("ExecuteFallbackGuideline present=%v, want %v\nprompt:\n%s", hasFallback, tt.wantFallback, got)
			}
		})
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The "prefer search_files over execute+grep" guideline compares two tools, so
// it is only sound when both are present: a run without execute must not be
// told about a tool it does not have.
func TestSearchOverExecuteGuidelineNeedsBothTools(t *testing.T) {
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	pick := func(names ...string) []core.Tool {
		var out []core.Tool
		for _, tl := range all {
			for _, n := range names {
				if tl.Name == n {
					out = append(out, tl)
				}
			}
		}
		if len(out) != len(names) {
			t.Fatalf("wanted %v, resolved %d tools", names, len(out))
		}
		return out
	}

	for _, tc := range []struct {
		name  string
		tools []core.Tool
		want  bool
	}{
		{"search_files and execute", pick("search_files", "execute"), true},
		{"search_files without execute", pick("search_files", "list_files"), false},
		{"execute without search_files", pick("execute", "list_files"), false},
		{"search_files alone", pick("search_files"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Build("", tc.tools)
			if has := contains(got, "execute+grep"); has != tc.want {
				t.Errorf("execute+grep guideline present=%v, want %v\nprompt:\n%s", has, tc.want, got)
			}
		})
	}
}

// Issue #75 §4: a custom system prompt replaces the BUILT-IN text — the base
// instructions and the universal guidelines — but a tool's own guidelines
// travel with the tool (NFR-TEST-08a), so they still reach the model.
func TestACustomPromptKeepsTheToolsGuidelines(t *testing.T) {
	got := Build("You are a release engineer.", []core.Tool{
		{Name: "search_files", PromptGuidelines: []string{"Search before reading whole files."}},
		{Name: "execute"},
	})
	for _, want := range []string{"You are a release engineer.", "Search before reading whole files.", tools.SearchOverExecuteGuideline} {
		if !strings.Contains(got, want) {
			t.Errorf("custom prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, BaseInstructions) {
		t.Error("a custom prompt must replace the built-in base instructions")
	}
	for _, g := range UniversalGuidelines {
		if strings.Contains(got, g) {
			t.Errorf("a custom prompt must replace the built-in universal guideline %q", g)
		}
	}
}

// Issue #75 §4: the shell guidelines are keyed on whichever shell tool is
// active, not on the literal name "execute", and name that tool.
func TestShellGuidelinesNameTheActiveShell(t *testing.T) {
	for _, shell := range []string{"run_command"} {
		got := Build("", []core.Tool{{Name: shell}})
		if want := "Use " + shell + " for file operations like ls, rg, find."; !strings.Contains(got, want) {
			t.Errorf("%s alone: prompt lacks %q:\n%s", shell, want, got)
		}
		got = Build("", []core.Tool{{Name: "search_files"}, {Name: shell}})
		if want := "Prefer search_files over " + shell + "+grep"; !strings.Contains(got, want) {
			t.Errorf("%s with search_files: prompt lacks %q:\n%s", shell, want, got)
		}
	}
	// execute keeps its exact, pinned wording.
	if got := Build("", []core.Tool{{Name: "execute"}}); !strings.Contains(got, tools.ExecuteFallbackGuideline) {
		t.Errorf("execute alone: prompt lacks the pinned %q", tools.ExecuteFallbackGuideline)
	}
}

// TS-12-35: Build takes the base prompt and the tools; Input, SkillBlocks
// and the skills package are gone.
func TestBuildSignatureAndNoSkills_TS12_35(t *testing.T) {
	var build func(string, []core.Tool) string = Build
	if build == nil {
		t.Fatal("Build is nil")
	}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			for _, imp := range f.Imports {
				if strings.Contains(imp.Path.Value, "/skills") {
					t.Errorf("%s imports %s", name, imp.Path.Value)
				}
			}
			for _, d := range f.Decls {
				if g, ok := d.(*ast.GenDecl); ok {
					for _, sp := range g.Specs {
						if ts, ok := sp.(*ast.TypeSpec); ok && (ts.Name.Name == "Input" || ts.Name.Name == "SkillBlocks") {
							t.Errorf("prompt still declares %s", ts.Name.Name)
						}
					}
				}
			}
		}
	}
}

// TS-12-36: an empty system prompt opens with the built-in instructions and
// universal guidelines; a non-empty one replaces both.
func TestBuildBaseOrCustom_TS12_36(t *testing.T) {
	def := Build("", nil)
	if !strings.HasPrefix(def, BaseInstructions) || !strings.Contains(def, UniversalGuidelines[0]) {
		t.Fatalf("default prompt:\n%s", def)
	}
	custom := Build("Custom system prompt instructions.", nil)
	if !strings.HasPrefix(custom, "Custom system prompt instructions.") || strings.Contains(custom, BaseInstructions) ||
		strings.Contains(custom, UniversalGuidelines[0]) {
		t.Fatalf("custom prompt:\n%s", custom)
	}
}

// TS-12-37: tool guidelines are deduplicated in first-seen order, and the
// shell guidelines follow the tools present.
func TestBuildGuidelines_TS12_37(t *testing.T) {
	t1 := core.Tool{Name: "t1", PromptGuidelines: []string{"Guideline A", "Guideline B"}}
	t2 := core.Tool{Name: "t2", PromptGuidelines: []string{"Guideline B", "Guideline C"}}
	res := Build("", []core.Tool{t1, t2})
	a, b, c := strings.Index(res, "Guideline A"), strings.Index(res, "Guideline B"), strings.Index(res, "Guideline C")
	if !(a >= 0 && a < b && b < c) || strings.Count(res, "Guideline B") != 1 {
		t.Fatalf("guidelines out of order or repeated:\n%s", res)
	}
	alone := Build("", []core.Tool{{Name: "execute"}})
	if !strings.Contains(alone, tools.ExecuteFallbackGuideline) || strings.Contains(alone, tools.SearchOverExecuteGuideline) {
		t.Fatalf("execute alone:\n%s", alone)
	}
	both := Build("", []core.Tool{{Name: "search_files"}, {Name: "execute"}})
	if !strings.Contains(both, tools.SearchOverExecuteGuideline) {
		t.Fatalf("execute with search_files:\n%s", both)
	}
}
