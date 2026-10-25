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

// isNone reports whether f is a recognised file with no outline.
func isNone(f File) bool { return f.Backend == BackendNone && f.Decls != nil && len(f.Decls) == 0 }
