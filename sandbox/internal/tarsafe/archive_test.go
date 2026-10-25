package tarsafe

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// The archive keeps hidden files and symlinks as they are, names entries
// relative to the subtree, and extracts back to the same tree.
func TestArchiveWritesTheSubtreeVerbatim(t *testing.T) {
	src := t.TempDir()
	for name, body := range map[string]string{"sub/.hidden": "h", "sub/d/f": "f"} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "sub", "abs")); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string][]string{
		"sub": {".hidden", "abs", "d/", "d/f"},
		".":   {"sub/", "sub/.hidden", "sub/abs", "sub/d/", "sub/d/f"},
	} {
		var buf bytes.Buffer
		if err := Archive(openRoot(t, src), rel, &buf); err != nil {
			t.Fatal(err)
		}
		if got := names(t, buf.Bytes()); !slices.Equal(got, want) {
			t.Errorf("Archive(%q) holds %v, want %v", rel, got, want)
		}
	}
	var buf bytes.Buffer
	if err := Archive(openRoot(t, src), "sub/d", &buf); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if _, err := Extract(openRoot(t, dst), &buf); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "f")); string(b) != "f" {
		t.Errorf("the extracted f holds %q", b)
	}
}

func TestArchiveOfASingleFileUsesItsBaseName(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Archive(openRoot(t, src), "a/f.txt", &buf); err != nil {
		t.Fatal(err)
	}
	if got := names(t, buf.Bytes()); !slices.Equal(got, []string{"f.txt"}) {
		t.Errorf("Archive holds %v", got)
	}
	if err := Archive(openRoot(t, src), "missing", &buf); !os.IsNotExist(err) {
		t.Errorf("Archive of a missing path = %v, want not-exist", err)
	}
}
