package tools

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestCandidateFilesAreTheVisibleTextFilesThatMentionTheName(t *testing.T) {
	ws := newWS(t, map[string]string{
		".gitignore": "build/\n*.log\n", "src/app.py": "def start():\n    Run()\n", "src/util.ts": "export function exec() { Run(); }\n",
		"src/ignored.log": "Run log\n", "build/bundle.js": "function Run() {}\n", ".git/config": "Run\n", ".hidden.py": "Run()\n",
		"blob.bin": "Run\x00", "src/none.py": "nothing here\n",
	})
	got, err := findCandidateFiles(context.Background(), ws, "Run", nil, nil)
	if err != nil || !slices.Equal(got, []string{"src/app.py", "src/util.ts"}) {
		t.Fatalf("candidates %v, %v", got, err)
	}
	// An index that knows the candidates answers instead, under the same rules.
	idx := &candidateFakeIndex{cands: []string{".git/config", "build/bundle.js", ".hidden.py", "src/ignored.log",
		"../outside.go", "/abs.go", "src", "src/gone.py", "src/app.py", "./src/util.ts"}}
	if got, err := findCandidateFiles(context.Background(), ws, "Run", idx, nil); err != nil || !slices.Equal(got, []string{"src/app.py", "src/util.ts"}) {
		t.Fatalf("indexed candidates %v, %v", got, err)
	}
	// A budget that refuses keeps what was found so far.
	b := newRefBudget(SymbolOptions{MaxFiles: 1})
	if got, _ := findCandidateFiles(context.Background(), ws, "Run", idx, b); len(got) != 0 || b.partialReason() != "files" {
		t.Fatalf("a one-file budget over the index: %v, %q", got, b.partialReason())
	}
	if _, err := findCandidateFiles(context.Background(), nil, "Run", nil, nil); err == nil {
		t.Fatal("a nil workspace")
	}
}

func TestTheScannerMatchesWholeIdentifiersAtOneBasedColumns(t *testing.T) {
	for _, c := range []struct {
		path, content, name string
		want                []string
	}{
		{"t.py", "def run_all():\n    runner.run()\n    runaway = 1\n", "run", []string{"2:12"}},
		{"t.ts", "let x = computeTarget();\n🚀 computeTarget + 1;\n/* こんにちは */ v = computeTarget", "computeTarget", []string{"1:9", "2:6", "3:27"}},
		{"t.js", "$run(); run$(); run();", "run", []string{"1:17"}}, // $ is part of a JavaScript identifier
		{"t.txt", "a $run b", "run", []string{"1:4"}},
		{"t.py", "nothing", "", nil},
	} {
		var got []string
		for _, s := range scanContentForMatches(c.path, []byte(c.content), outline.Decl{Name: c.name}) {
			got = append(got, fmt.Sprintf("%d:%d", s.Line, s.Column))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s %q: %v, want %v", c.path, c.content, got, c.want)
		}
	}
}

func TestACodeHitIsLexicalAndACommentOrStringHitIsText(t *testing.T) {
	sites := scanContentForMatches("t.py", []byte("# call execute here\ns = \"call execute\"\nservice.execute()\n"), outline.Decl{Name: "execute"})
	conf := []string{}
	for _, s := range sites {
		conf = append(conf, s.Confidence)
	}
	want := []string{"text", "text", "lexical"}
	if _, classified := outline.CommentAndStringSpans(context.Background(), "t.py", nil); !classified {
		want = []string{"text", "text", "text"} // no grammar without cgo
	}
	if !slices.Equal(conf, want) {
		t.Fatalf("confidences %v, want %v", conf, want)
	}
	// Markdown has no grammar, and a name searched as text is text everywhere.
	ws := newWS(t, map[string]string{"README.md": "Please execute this.\n", "a.py": "service.execute()\n"})
	res, err := executeReferenceSearch(context.Background(), ws, outline.Decl{Name: "execute"}, "", "text", ReferenceOptions{IncludeTests: true}, SymbolOptions{}, nil)
	if err != nil || len(res.Sites) != 2 || slices.ContainsFunc(res.Sites, func(s ReferenceSite) bool { return s.Confidence != "text" }) {
		t.Fatalf("%v, %v", siteList(res), err)
	}
}
