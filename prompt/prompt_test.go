package prompt

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// defaultToolSet is the real default tool set through the real resolver, so a
// change to any tool's description or guidelines shows up as a golden diff.
func defaultToolSet(t *testing.T) []core.Tool {
	t.Helper()
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return core.ToolPolicy{}.Resolve(all)
}

func named(names ...string) []core.Tool {
	out := make([]core.Tool, len(names))
	for i, n := range names {
		out[i] = core.Tool{Name: n}
	}
	return out
}

func TestBuildMatchesTheGoldenPromptOverTheDefaultTools(t *testing.T) {
	navigation := map[string]bool{"list_files": true, "find_files": true, "search_files": true,
		"file_outline": true, "find_symbol": true, "find_references": true}
	all := defaultToolSet(t)
	var withoutNavigation []core.Tool
	for _, tl := range all {
		if !navigation[tl.Name] {
			withoutNavigation = append(withoutNavigation, tl)
		}
	}
	for _, tc := range []struct {
		golden, system string
		tools          []core.Tool
	}{
		{"system_prompt_default.txt", "", all},
		{"system_prompt_custom.txt", "You are a release engineer. Answer only about this repository.", all},
		{"system_prompt_no_navigation.txt", "", withoutNavigation},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			testutil.Golden(t, filepath.Join("testdata", "golden", tc.golden), []byte(Build(tc.system, tc.tools)))
		})
	}
}

func TestBuildWithoutASystemPromptOpensWithTheBaseAndEndsWithTheUniversalGuidelines(t *testing.T) {
	got := Build("", nil)
	want := BaseInstructions + "\n\nGuidelines:\n- " + strings.Join(UniversalGuidelines, "\n- ")
	if got != want {
		t.Fatalf("Build(\"\", nil) =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildWithASystemPromptReplacesTheBuiltInTextButKeepsTheToolsGuidelines(t *testing.T) {
	if got := Build("Custom.\n\n", nil); got != "Custom." {
		t.Fatalf("custom prompt without guidelines = %q, want it alone, trailing newlines trimmed", got)
	}
	got := Build("You are a release engineer.", []core.Tool{
		{Name: "search_files", PromptGuidelines: []string{"Search before reading whole files."}},
		{Name: "execute"},
	})
	want := "You are a release engineer.\n\nGuidelines:\n" +
		"- Search before reading whole files.\n- " + tools.SearchOverExecuteGuideline
	if got != want {
		t.Fatalf("custom prompt =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildDeduplicatesGuidelinesInFirstSeenOrder(t *testing.T) {
	got := Build("", []core.Tool{
		{Name: "zeta", PromptGuidelines: []string{"Zeta first.", "Shared advice.", "  "}},
		{Name: "alpha", PromptGuidelines: []string{" Shared advice. ", "Alpha second."}},
	})
	want := BaseInstructions + "\n\nGuidelines:\n" +
		"- Zeta first.\n- Shared advice.\n- Alpha second.\n- " + strings.Join(UniversalGuidelines, "\n- ")
	if got != want {
		t.Fatalf("Build =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildAddsTheShellFallbackOnlyWhenNoNavigationToolIsPresent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []core.Tool
		want  bool
	}{
		{"execute alone", named("execute"), true},
		{"execute with file_outline", named("execute", "file_outline"), false},
		{"execute with find_symbol", named("execute", "find_symbol"), false},
		{"execute with list_files", named("execute", "list_files"), false},
		{"no shell", named("read_file"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Build("", tc.tools)
			if has := strings.Contains(got, tools.ExecuteFallbackGuideline); has != tc.want {
				t.Errorf("fallback present = %v, want %v\n%s", has, tc.want, got)
			}
		})
	}
}

func TestBuildPrefersSearchFilesOverTheShellOnlyWhenBothArePresent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []core.Tool
		want  bool
	}{
		{"search_files and execute", named("search_files", "execute"), true},
		{"search_files without a shell", named("search_files", "list_files"), false},
		{"execute without search_files", named("execute", "list_files"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Build("", tc.tools)
			if has := strings.Contains(got, tools.SearchOverExecuteGuideline); has != tc.want {
				t.Errorf("search guideline present = %v, want %v\n%s", has, tc.want, got)
			}
		})
	}
}

func TestBuildNamesTheActiveShellInTheShellGuidelines(t *testing.T) {
	got := Build("", named("run_command"))
	if want := "Use run_command for file operations like ls, rg, find."; !strings.Contains(got, want) {
		t.Errorf("run_command alone: prompt lacks %q:\n%s", want, got)
	}
	got = Build("", named("search_files", "run_command"))
	if want := "Prefer search_files over run_command+grep"; !strings.Contains(got, want) {
		t.Errorf("run_command with search_files: prompt lacks %q:\n%s", want, got)
	}
	if strings.Contains(got, "execute") {
		t.Errorf("a prompt without execute names it:\n%s", got)
	}
}
