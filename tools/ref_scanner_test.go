package tools

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
)

type mockCandidateIndex struct {
	candidates []string
}

func (m *mockCandidateIndex) Symbols(ctx context.Context, q SymbolQuery) (SymbolAnswer, bool, error) {
	return SymbolAnswer{}, false, nil
}

func (m *mockCandidateIndex) Tools() []core.Tool {
	return nil
}

func (m *mockCandidateIndex) Invalidate(rel string) {}

func (m *mockCandidateIndex) Close() error {
	return nil
}

func (m *mockCandidateIndex) CandidateFiles(ctx context.Context, name string) ([]string, error) {
	return m.candidates, nil
}

// scanFile scans the file at path for whole-identifier occurrences of name.
func scanFile(t *testing.T, path, name string) []ReferenceSite {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return scanContentForMatches(path, content, outline.Decl{Name: name})
}

// TS-05-19 (unit): Reference scanner candidate traversal obeys .gitignore rules, hidden directory exclusions, and index filtering
// Verifies: 05-REQ-4.1
func TestRefScanner_CandidatesTraversal_TS_05_19(t *testing.T) {
	dir := t.TempDir()

	// .gitignore ignoring build/
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Active files in src/
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "app.py"), []byte("def start():\n    Run()\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "util.ts"), []byte("export function exec() { Run(); }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "ignored.log"), []byte("Run log output\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Ignored build/ directory
	if err := os.MkdirAll(filepath.Join(dir, "build"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build", "bundle.js"), []byte("function Run() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hidden directory .git/ and hidden file .hidden.py
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("Run config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden.py"), []byte("Run()\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	// Case 1: Traversal without index
	candidates, err := findCandidateFiles(context.Background(), ws, "Run", nil)
	if err != nil {
		t.Fatalf("findCandidateFiles failed: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("expected candidate files, got 0")
	}

	for _, c := range candidates {
		if strings.HasPrefix(c, ".git/") || strings.HasPrefix(c, ".git") {
			t.Fatalf("candidate %q should not be inside .git/", c)
		}
		if strings.HasPrefix(c, "build/") {
			t.Fatalf("candidate %q should not be inside build/", c)
		}
		if strings.HasPrefix(c, ".") {
			t.Fatalf("candidate %q should not be hidden", c)
		}
		if strings.HasSuffix(c, ".log") {
			t.Fatalf("candidate %q should not be ignored *.log", c)
		}
	}

	// Case 2: Traversal with mockIndex returning both valid and invalid candidates
	mockIndex := &mockCandidateIndex{
		candidates: []string{
			".git/config",
			"build/bundle.js",
			".hidden.py",
			"src/ignored.log",
			"src/app.py",
			"src/util.ts",
		},
	}
	candidatesIndexed, err := findCandidateFiles(context.Background(), ws, "Run", mockIndex)
	if err != nil {
		t.Fatalf("findCandidateFiles with index failed: %v", err)
	}
	for _, c := range candidatesIndexed {
		if strings.HasPrefix(c, ".git/") || strings.HasPrefix(c, "build/") || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".log") {
			t.Fatalf("indexed candidate %q should have been filtered out", c)
		}
	}
}

// TS-05-20 (unit): Reference scanner uses whole-word identifier boundaries for outline-supported languages
// Verifies: 05-REQ-4.2
func TestRefScanner_WholeWordBoundaries_TS_05_20(t *testing.T) {
	dir := t.TempDir()
	pyFile := filepath.Join(dir, "test.py")
	content := "def run_all():\n    runner.run()\n    runaway = 1\n"
	if err := os.WriteFile(pyFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	matches := scanFile(t, pyFile, "run")
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 match, got %d: %+v", len(matches), matches)
	}
	if m := matches[0]; m.Line != 2 || m.Column != 12 || m.Source != "runner.run()" {
		t.Fatalf("expected runner.run() at 2:12, got %d:%d %q", m.Line, m.Column, m.Source)
	}
}

// codeConfidence is the confidence of a hit outside comments and strings:
// lexical where the language's grammar classifies the file, text in a build
// without cgo.
func codeConfidence(path string) string {
	if _, ok := outline.CommentAndStringSpans(context.Background(), path, nil); ok {
		return "lexical"
	}
	return "text"
}

// TS-05-21 (unit): Reference scanner classifies code matches outside comments and strings in outline files as lexical
// Verifies: 05-REQ-4.3
func TestRefScanner_LexicalConfidence_TS_05_21(t *testing.T) {
	targetDecl := outline.Decl{
		Kind: "func",
		Name: "execute",
	}

	sites := scanContentForMatches("app.ts", []byte("service.execute()"), targetDecl)
	if len(sites) != 1 {
		t.Fatalf("expected 1 site, got %d", len(sites))
	}
	site := sites[0]
	if want := codeConfidence("app.ts"); site.Confidence != want {
		t.Fatalf("expected confidence %q, got %q", want, site.Confidence)
	}
	if site.Path != "app.ts" {
		t.Fatalf("expected path 'app.ts', got %q", site.Path)
	}
	if site.Line != 1 {
		t.Fatalf("expected line 1, got %d", site.Line)
	}
	if site.Column != 9 {
		t.Fatalf("expected column 9, got %d", site.Column)
	}
}

// TS-05-22 (unit): Reference scanner classifies matches inside comments, strings, or unparseable files as text
// Verifies: 05-REQ-4.4
func TestRefScanner_CommentStringTextConfidence_TS_05_22(t *testing.T) {
	dir := t.TempDir()

	pyContent := "# call execute here\ns = \"call execute\"\nservice.execute()\n"
	if err := os.WriteFile(filepath.Join(dir, "test.py"), []byte(pyContent), 0o600); err != nil {
		t.Fatal(err)
	}

	mdContent := "# Readme\nPlease execute this command.\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(mdContent), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}
	targetDecl := outline.Decl{
		Kind: "func",
		Name: "execute",
	}

	res, err := ws.References(context.Background(), targetDecl, ReferenceOptions{IncludeTests: true, MaxResults: 100})
	if err != nil {
		t.Fatal(err)
	}
	sites := res.Sites
	if len(sites) < 3 {
		t.Fatalf("expected at least 3 sites, got %d", len(sites))
	}

	for _, s := range sites {
		inCommentOrString := strings.ContainsAny(s.Source, "#\"'") || strings.Contains(s.Source, "//") || strings.Contains(s.Source, "/*")
		if inCommentOrString || strings.HasSuffix(s.Path, ".md") {
			if s.Confidence != "text" {
				t.Fatalf("site in %s:%d (%s) expected confidence 'text', got %q", s.Path, s.Line, s.Source, s.Confidence)
			}
		} else if strings.HasSuffix(s.Path, ".py") && strings.Contains(s.Source, "service.execute()") {
			if want := codeConfidence(s.Path); s.Confidence != want {
				t.Fatalf("code site in %s:%d expected confidence %q, got %q", s.Path, s.Line, want, s.Confidence)
			}
		}
	}
}

// TS-05-23 (unit): Reference scanner classifies all sites as text when target declaration is not in the symbol table
// Verifies: 05-REQ-4.5
func TestRefScanner_UndeclaredTargetTextConfidence_TS_05_23(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nconst MY_GLOBAL_VAR = 10\nfunc f() { _ = MY_GLOBAL_VAR }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.py"), []byte("print(MY_GLOBAL_VAR)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("console.log(MY_GLOBAL_VAR);\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}
	// Target declaration not found in symbol table: find_references searches
	// for the bare name with the text backend.
	res, err := executeReferenceSearch(context.Background(), ws, outline.Decl{Name: "MY_GLOBAL_VAR"}, "text", ReferenceOptions{IncludeTests: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sites := res.Sites
	if len(sites) == 0 {
		t.Fatal("expected reference sites, got 0")
	}

	for _, s := range sites {
		if s.Confidence != "text" {
			t.Fatalf("expected all sites to have confidence 'text' when target is undeclared, got %q at %s:%d", s.Confidence, s.Path, s.Line)
		}
	}
}

// TS-05-24 (property): Reference match locator determines 1-based line number and 1-based byte column offset
// Verifies: 05-REQ-4.6
func TestRefScanner_LineColumnCoordinatesProperty_TS_05_24(t *testing.T) {
	dir := t.TempDir()
	targetName := "computeTarget"

	prefixes := []string{
		"let x = ",
		"    // 測試 ",
		"\tПривет, ",
		"🚀 ",
		"/* こんにちは */ val = ",
	}
	suffixes := []string{
		"()",
		" + 1;",
		"; // end",
		"",
	}

	var rawLines []string
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 50; i++ {
		pre := prefixes[rng.Intn(len(prefixes))]
		suf := suffixes[rng.Intn(len(suffixes))]
		line := pre + targetName + suf
		rawLines = append(rawLines, line)
	}

	filePath := filepath.Join(dir, "prop_test.ts")
	if err := os.WriteFile(filePath, []byte(strings.Join(rawLines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	matches := scanFile(t, filePath, targetName)
	if len(matches) != len(rawLines) {
		t.Fatalf("expected %d matches, got %d", len(rawLines), len(matches))
	}

	for _, match := range matches {
		if match.Line < 1 {
			t.Fatalf("line number must be 1-based (got %d)", match.Line)
		}
		if match.Column < 1 {
			t.Fatalf("column number must be 1-based (got %d)", match.Column)
		}

		rawLine := rawLines[match.Line-1]
		colIdx := match.Column - 1
		if colIdx+len(targetName) > len(rawLine) {
			t.Fatalf("match range %d..%d exceeds line length %d in line %q", colIdx, colIdx+len(targetName), len(rawLine), rawLine)
		}
		got := rawLine[colIdx : colIdx+len(targetName)]
		if got != targetName {
			t.Fatalf("expected matched slice %q, got %q at line %d, col %d in %q", targetName, got, match.Line, match.Column, rawLine)
		}
	}
}
