package providerconformance

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

	"github.com/agent-fox-dev/agentkit-go/catalog"
)

// catalogModelIDs are the model ids of every provider's every tier, from
// catalog.Select: the ids no file of the suite may write down.
func catalogModelIDs(t testing.TB) []string {
	t.Helper()
	var ids []string
	seen := map[string]bool{}
	for _, p := range catalog.Providers() {
		for _, tier := range []string{"SIMPLE", "STANDARD", "ADVANCED"} {
			sel, err := catalog.Select(p, tier, "")
			if err != nil {
				t.Fatalf("catalog.Select(%s, %s): %v", p, tier, err)
			}
			if id := sel.Model.ID; !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// literalsWithIDs returns the string literals of file that contain one of ids.
func literalsWithIDs(t testing.TB, file string, ids []string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		for _, id := range ids {
			if strings.Contains(s, id) {
				found = append(found, s)
				break
			}
		}
		return true
	})
	return found
}

// TS-30-5 (30-REQ-1.5): no file of the suite writes a model id of the catalog.
// Every expected id is read from catalog.Select(provider, "STANDARD", ""), so a
// change of tier is a change of the catalog and breaks nothing here.
func TestNoLiteralModelIDs_TS30_5(t *testing.T) {
	ids := catalogModelIDs(t)
	if len(ids) < 3 {
		t.Fatalf("catalog.Select gave %d distinct model ids %v, want one for each provider at least", len(ids), ids)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files in the package")
	}
	for _, f := range files {
		for _, lit := range literalsWithIDs(t, f, ids) {
			t.Errorf("%s writes the string %q, which holds a catalog model id; read it from catalog.Select", f, lit)
		}
	}
}

// The scan is what TS-30-5 stands on: it must see an id inside a longer string,
// in a raw string, and must not read a comment as a literal.
func TestLiteralScanSeesAnID_TS30_5(t *testing.T) {
	ids := catalogModelIDs(t)
	dir := t.TempDir()
	src := "package x\n// " + ids[0] + " in a comment\nvar a = \"model=" + ids[0] + "\"\nvar b = `" + ids[len(ids)-1] +
		"`\nvar c = \"claude\"\nvar d = 42\n"
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := literalsWithIDs(t, path, ids)
	if len(got) != 2 || got[0] != "model="+ids[0] || got[1] != ids[len(ids)-1] {
		t.Errorf("the scan found %q, want the two literals that hold an id", got)
	}
}

// The ids the scan forbids are every tier's of every provider, not the
// STANDARD tier's alone: a case that wrote the ADVANCED id would be as much a
// literal.
func TestCatalogModelIDsCoverEveryTier_TS30_5(t *testing.T) {
	ids := catalogModelIDs(t)
	for _, p := range catalog.Providers() {
		for _, tier := range []catalog.Tier{catalog.TierSimple, catalog.TierStandard, catalog.TierAdvanced} {
			sel, err := catalog.Select(p, string(tier), "")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(ids, sel.Model.ID) {
				t.Errorf("the forbidden ids %v lack %s's %s model %s", ids, p, tier, sel.Model.ID)
			}
		}
	}
}
