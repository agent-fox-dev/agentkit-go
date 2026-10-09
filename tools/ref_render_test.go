package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// TS-05-35 (unit): Result renderer outputs header with declaration name, backend, counts, and partial advisory
// Verifies: 05-REQ-7.1
func TestRefRender_TS05_35(t *testing.T) {
	// Given: reference query results with backend 'go/types', 5 references across 2 files, 1 text match, and Partial true
	res := ReferenceResult{
		Target: outline.Decl{
			Kind: outline.KindFunc,
			Name: "Run",
		},
		Backend: "go/types",
		Partial: true,
		Sites: []ReferenceSite{
			{Path: "pkg/a.go", Line: 10, Confidence: "resolved"},
			{Path: "pkg/a.go", Line: 20, Confidence: "resolved"},
			{Path: "pkg/a.go", Line: 30, Confidence: "resolved"},
			{Path: "pkg/b.go", Line: 5, Confidence: "resolved"},
			{Path: "pkg/b.go", Line: 15, Confidence: "text"},
		},
	}

	// When: the result is rendered
	txt := renderReferencesResult(res.Target.Name, res).Text

	// Then:
	// - first line format matches 'find_references <name>  (go/types, 5 references in 2 files; 1 text matches) [partial]'
	// - name and backend are accurately reflected in header
	lines := strings.Split(txt, "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	firstLine := lines[0]
	wantPrefix := "find_references Run  (go/types, 5 references in 2 files; 1 text matches)"
	if !strings.HasPrefix(firstLine, wantPrefix) {
		t.Fatalf("first line = %q, want prefix %q", firstLine, wantPrefix)
	}
	if !strings.Contains(firstLine, "partial") {
		t.Fatalf("first line = %q, want containing %q", firstLine, "partial")
	}
}

// TS-05-36 (unit): Result renderer groups reference sites under relative file paths with padded indented lines
// Verifies: 05-REQ-7.2
func TestRefRender_TS05_36(t *testing.T) {
	// Given: two reference sites in 'pkg/runner.go' at lines 12 and 45
	res := ReferenceResult{
		Target: outline.Decl{
			Kind: outline.KindMethod,
			Name: "Run",
		},
		Backend: "go/types",
		Sites: []ReferenceSite{
			{
				Path:       "pkg/runner.go",
				Line:       12,
				Confidence: "resolved",
				Enclosing: outline.Decl{
					Kind:      outline.KindFunc,
					Name:      "Start",
					Signature: "func Start() error",
				},
				Source: "runner.Run()",
			},
			{
				Path:       "pkg/runner.go",
				Line:       45,
				Confidence: "resolved",
				Enclosing: outline.Decl{
					Kind:      outline.KindFunc,
					Name:      "Stop",
					Signature: "func Stop()",
				},
				Source: "runner.Run()",
			},
		},
	}

	// When: the result is rendered
	txt := renderReferencesResult(res.Target.Name, res).Text

	// Then:
	// - file path 'pkg/runner.go' appears as a header line
	// - sites appear indented with format '  L<line>  <confidence>  <enclosing>  <source_line>'
	// - enclosing declaration column is padded for alignment across lines in the group
	if !strings.Contains(txt, "pkg/runner.go\n  L12  resolved  func Start() error      runner.Run()") {
		t.Fatalf("output missing expected line 12 format:\n%s", txt)
	}
	if !strings.Contains(txt, "  L45  resolved  func Stop()") {
		t.Fatalf("output missing expected line 45 format:\n%s", txt)
	}

	// Verify alignment across lines in the group
	lines := strings.Split(txt, "\n")
	var l12Col, l45Col int
	for _, l := range lines {
		if strings.Contains(l, "L12") {
			l12Col = strings.Index(l, "runner.Run()")
		}
		if strings.Contains(l, "L45") {
			l45Col = strings.Index(l, "runner.Run()")
		}
	}
	if l12Col == 0 || l45Col == 0 || l12Col != l45Col {
		t.Fatalf("enclosing declarations not aligned: l12Col=%d l45Col=%d in:\n%s", l12Col, l45Col, txt)
	}
}

// TS-05-37 (unit): Result renderer appends truncation and partial markers on distinct lines at the conclusion of text
// Verifies: 05-REQ-7.3
func TestRefRender_TS05_37(t *testing.T) {
	// Given: a result with Truncated true and Partial true
	truncatedPartialRes := ReferenceResult{
		Target: outline.Decl{
			Kind: outline.KindFunc,
			Name: "Run",
		},
		Backend:   "go/types",
		Truncated: true,
		Partial:   true,
		Sites: []ReferenceSite{
			{Path: "pkg/a.go", Line: 10, Confidence: "resolved", Source: "Run()"},
		},
	}

	// When: the result is rendered
	txt := renderReferencesResult("Run", truncatedPartialRes).Text

	// Then:
	// - truncation CapMarker appears on its own line after the file groups
	// - SymbolPartialMarker appears on its own line after the file groups
	// - markers are separated from site listings
	lines := strings.Split(strings.TrimSpace(txt), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 lines, got %d:\n%s", len(lines), txt)
	}
	wantCapMarker := CapMarker("references", "max_results", 1, 100, "narrow with path or kind")
	hasCap := strings.Contains(lines[len(lines)-2], wantCapMarker) || strings.Contains(lines[len(lines)-1], wantCapMarker)
	if !hasCap {
		t.Fatalf("expected CapMarker at conclusion, got lines: %q and %q (want %q)", lines[len(lines)-2], lines[len(lines)-1], wantCapMarker)
	}
	if !strings.Contains(txt, SymbolPartialMarker("")) {
		t.Fatalf("expected partial marker %q in output:\n%s", SymbolPartialMarker(""), txt)
	}
}

// TS-05-38 (unit): Result renderer populates ToolResult.Data with complete ReferenceResult structure
// Verifies: 05-REQ-7.4
func TestRefRender_TS05_38(t *testing.T) {
	// Given: a completed reference resolution run
	refResult := ReferenceResult{
		Target: outline.Decl{
			Kind: outline.KindFunc,
			Name: "Start",
		},
		Sites: []ReferenceSite{
			{Path: "pkg/runner.go", Line: 12, Confidence: "resolved", Source: "Start()"},
		},
		Backend:         "go/types",
		Partial:         false,
		Truncated:       false,
		PackagesChecked: 3,
		Errors:          0,
	}

	// When: ToolResult.Data is inspected
	res := renderReferencesResult("Start", refResult)

	// Then:
	// - Data can be asserted as ReferenceResult
	// - Target, Sites, Backend, Partial, Truncated, PackagesChecked, and Errors are populated with appropriate values
	data, ok := res.Data["result"].(ReferenceResult)
	if !ok {
		t.Fatalf("res.Data[\"result\"] cannot be asserted as ReferenceResult, got %T", res.Data["result"])
	}
	if data.Backend == "" {
		t.Fatal("expected non-empty Backend")
	}
	if data.PackagesChecked < 0 || data.Errors < 0 {
		t.Fatalf("expected non-negative PackagesChecked and Errors, got %d and %d", data.PackagesChecked, data.Errors)
	}
	if data.Target.Name != "Start" {
		t.Fatalf("expected Target.Name = %q, got %q", "Start", data.Target.Name)
	}
	if len(data.Sites) != 1 {
		t.Fatalf("expected 1 site, got %d", len(data.Sites))
	}
	if data.Partial != false || data.Truncated != false {
		t.Fatalf("expected Partial=false Truncated=false, got %v and %v", data.Partial, data.Truncated)
	}

	// Also verify individual Data map keys per 05-REQ-7.4
	if res.Data["backend"] != "go/types" {
		t.Fatalf("res.Data[\"backend\"] = %v, want go/types", res.Data["backend"])
	}
	if res.Data["packages_checked"] != 3 {
		t.Fatalf("res.Data[\"packages_checked\"] = %v, want 3", res.Data["packages_checked"])
	}
	if res.Data["errors"] != 0 {
		t.Fatalf("res.Data[\"errors\"] = %v, want 0", res.Data["errors"])
	}
}

// TS-05-39 (unit): Result renderer returns successful ToolResult with zero references indicated when no matches exist
// Verifies: 05-REQ-7.5
func TestRefRender_TS05_39(t *testing.T) {
	// Given: a workspace where no file contains the queried identifier (zero matches result)
	emptyRes := ReferenceResult{
		Target: outline.Decl{
			Kind: outline.KindFunc,
			Name: "NeverUsed",
		},
		Backend: "text",
		Sites:   []ReferenceSite{},
	}

	// When: find_references result renderer executes
	res := renderReferencesResult("NeverUsed", emptyRes)

	// Then:
	// - ToolResult has OK true
	// - Text indicates zero references found
	// - Data contains an empty Sites slice (len == 0)
	if !res.OK {
		t.Fatalf("expected res.OK == true, got %v", res.OK)
	}
	data, ok := res.Data["result"].(ReferenceResult)
	if !ok {
		t.Fatalf("expected res.Data[\"result\"] to be ReferenceResult, got %T", res.Data["result"])
	}
	if len(data.Sites) != 0 {
		t.Fatalf("expected len(data.Sites) == 0, got %d", len(data.Sites))
	}
	if sites, ok := res.Data["sites"].([]ReferenceSite); !ok || len(sites) != 0 {
		t.Fatalf("expected empty sites slice in res.Data[\"sites\"], got %v", res.Data["sites"])
	}
	if !strings.Contains(res.Text, "0 references") {
		t.Fatalf("expected res.Text to contain '0 references', got:\n%s", res.Text)
	}
}

// TS-05-38 (unit): a real search reports how many package checks it used
// and the type errors they collected.
// Verifies: 05-REQ-7.4
func TestRefRender_CountsFromSearch_TS05_38(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/c\n\ngo 1.22\n")
	writeFile(t, root, "a/a.go", "package a\n\nimport \"fmt\"\n\nfunc Hello() { fmt.Println(\"hi\") }\n")
	writeFile(t, root, "b/b.go", "package b\n\nimport \"example.com/c/a\"\n\nfunc Use() { a.Hello() }\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ws.References(context.Background(), outline.Decl{Kind: outline.KindFunc, Name: "Hello", StartLine: 5}, ReferenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.PackagesChecked < 2 {
		t.Fatalf("PackagesChecked = %d, want at least the two workspace packages", res.PackagesChecked)
	}
	if res.Errors == 0 {
		t.Fatal("Errors = 0, want the error from fmt.Println on the stubbed fmt package")
	}
}
