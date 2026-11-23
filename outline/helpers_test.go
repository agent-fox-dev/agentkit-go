package outline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// show renders a declaration as "kind Container.Name L<start>-<end>", with a
// trailing "-" when it is not exported.
func show(d Decl) string {
	name := d.Name
	if d.Container != "" {
		name = d.Container + "." + name
	}
	s := fmt.Sprintf("%s %s L%d-%d", d.Kind, name, d.StartLine, d.EndLine)
	if !d.Exported {
		s += " -"
	}
	return s
}

func shows(decls []Decl) []string {
	out := []string{}
	for _, d := range decls {
		out = append(out, show(d))
	}
	return out
}

// outlineSrc outlines src as the file /x/<name>.
func outlineSrc(t *testing.T, name, src string) File {
	t.Helper()
	f, err := Outline(context.Background(), filepath.Join("/x", name), []byte(src), Options{})
	if err != nil {
		t.Fatalf("Outline(%s): %v", name, err)
	}
	return f
}

// isNone reports whether f is a file that was not parsed, for the given
// reason: no backend, no declarations, no syntax errors, both slices
// non-nil.
func isNone(f File, reason NotParsedReason) bool {
	return f.Backend == BackendNone && !f.Parsed && f.NotParsed == reason &&
		f.Decls != nil && len(f.Decls) == 0 &&
		f.SyntaxErrors != nil && len(f.SyntaxErrors) == 0 && !f.SyntaxErrorsTruncated
}

// assertParsedClean fails the test unless f was parsed with no reason and
// no syntax errors.
func assertParsedClean(t *testing.T, f File) {
	t.Helper()
	if !f.Parsed || f.NotParsed != "" || f.SyntaxErrors == nil || len(f.SyntaxErrors) != 0 || f.SyntaxErrorsTruncated {
		t.Errorf("%s: Parsed %v, NotParsed %q, SyntaxErrors %#v (truncated %v); want parsed with no errors",
			f.Path, f.Parsed, f.NotParsed, f.SyntaxErrors, f.SyntaxErrorsTruncated)
	}
}
