package tools

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestFindReferencesRendersTheSitesFileByFile(t *testing.T) {
	start := outline.Decl{Kind: outline.KindFunc, Name: "Start", Signature: "func Start() error"}
	stop := outline.Decl{Kind: outline.KindFunc, Name: "Stop", Signature: "func Stop()"}
	for _, c := range []struct {
		name string
		res  ReferenceResult
		want string
	}{
		{"labels are padded so the sources align", ReferenceResult{Target: outline.Decl{Kind: outline.KindMethod, Name: "Run"}, Backend: "go/types", Sites: []ReferenceSite{
			{Path: "pkg/runner.go", Line: 12, Confidence: "resolved", Enclosing: start, Source: "runner.Run()"},
			{Path: "pkg/runner.go", Line: 45, Confidence: "resolved", Enclosing: stop, Source: "runner.Run()"},
			{Path: "pkg/b.go", Line: 5, Confidence: "text", Enclosing: outline.Decl{Kind: "file"}},
		}}, "find_references Run  (go/types, 3 references in 2 files; 1 text matches)\n" +
			"pkg/runner.go\n  L12  resolved  func Start() error      runner.Run()\n  L45  resolved  func Stop()             runner.Run()\n" +
			"pkg/b.go\n  L5  text      <file>"},
		{"truncated and partial", ReferenceResult{Target: outline.Decl{Name: "Run"}, Backend: "go/types", Truncated: true, Partial: true, partialReason: "time",
			Sites: []ReferenceSite{{Path: "a.go", Line: 10, Confidence: "resolved", Source: "Run()"}}},
			"find_references Run  (go/types, 1 references in 1 files; 0 text matches) [partial]\na.go\n  L10  resolved  <file>      Run()\n" +
				CapMarker("references", "max_results", 1, 100, "narrow with path or kind") + "\n" + SymbolPartialMarker("time")},
		{"no declaration and no sites", ReferenceResult{Backend: "text"},
			"find_references NeverUsed  (text, 0 references in 0 files; 0 text matches) [0 declarations matched]"},
	} {
		name := c.res.Target.Name
		if name == "" {
			name = "NeverUsed"
		}
		r := renderReferencesResult(name, c.res)
		if !r.OK || r.Text != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, r.Text, c.want)
		}
		if sites, ok := r.Data["sites"].([]ReferenceSite); !ok || sites == nil || r.Data["backend"] != c.res.Backend ||
			r.Data["truncated"] != c.res.Truncated || r.Data["partial"] != c.res.Partial || (r.Metadata != nil) != c.res.Truncated {
			t.Errorf("%s: Data %v", c.name, r.Data)
		}
	}
	r := renderReferencesResult("Start", ReferenceResult{Target: start, Backend: "go/types", PackagesChecked: 3, Errors: 1})
	if res := r.Data["result"].(ReferenceResult); res.Target != start || r.Data["packages_checked"] != 3 || r.Data["errors"] != 1 || r.Data["target"] != start {
		t.Errorf("Data %v", r.Data)
	}
}
