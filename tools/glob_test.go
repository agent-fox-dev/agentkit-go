package tools

import (
	"slices"
	"testing"
)

func TestMatchGlobDialect(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "sub/main.go", true}, // a bare pattern also matches by basename
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true}, // ** matches zero segments
		{"a/**/c.go", "a/b/x/c.go", true},
		{"a/**/c.go", "a/c.go", true},
		{"{a,b}/*.go", "a/x.go", true},
		{"{a,b}/*.go", "c/x.go", false},
		{"{a,{b,c}}/x.go", "c/x.go", true},
		{"src/*.go", "src/sub/x.go", false}, // * does not cross /
		{"*.go", "Main.GO", true},           // smart-case: a lowercase pattern folds case
		{"*.Go", "main.go", false},
		{"*.Go", "main.Go", true},
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"a/**", "ab/c", false},
		{"*", ".env", true},
		{"**/*.go", "a/.hidden/x.go", true},
		{"[!x]*.go", "main.go", true},
		{"[!x]*.go", "xmain.go", false},
		{"[!x]*.go", "!main.go", true},
		{"{*.go,src/*.md}", "sub/x.go", true}, // basename fallback per alternative
		{"{*.go,src/*.md}", "sub/src/x.md", false},
		{"?.go", "ab.go", false},
		{"{a.go", "{a.go", true}, // an unbalanced brace is literal
		{"[", "[", false},        // a malformed pattern matches nothing
	}
	for _, c := range cases {
		if got := MatchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
	if got := ExpandBraces("x{a,b{c,d}}y"); !slices.Equal(got, []string{"xay", "xbcy", "xbdy"}) {
		t.Errorf("ExpandBraces = %v", got)
	}
}
