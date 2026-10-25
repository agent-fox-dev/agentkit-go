package tarsafe

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// entry is one tar entry to write: a header and, for a regular file, its body.
type entry struct {
	hdr  tar.Header
	body string
}

func reg(name, body string, mode int64) entry {
	return entry{tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: mode, Size: int64(len(body))}, body}
}

func folder(name string) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o755}}
}

func symlink(name, target string) entry {
	return entry{hdr: tar.Header{Typeflag: tar.TypeSymlink, Name: name, Linkname: target, Mode: 0o777}}
}

func tarOf(t *testing.T, es ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := e.hdr
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("writing %q: %v", h.Name, err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// target is a fresh target directory opened as a root, beside an empty
// directory "outside" (returned) that nothing may write to.
func target(t *testing.T) (*os.Root, string, string) {
	t.Helper()
	base := t.TempDir()
	dir, outside := filepath.Join(base, "target"), filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	t.Cleanup(func() {
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Errorf("the outside directory gained %v", ents)
		}
	})
	return root, dir, outside
}
