package tarsafe

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-13-50: extraction writes go through os.Root, so neither a pre-existing
// nor an earlier-extracted symlink lets a write escape the target.
// 13-REQ-8.3.
func TestRootWritesConfined_TS13_50(t *testing.T) {
	base := t.TempDir()
	target, outside := filepath.Join(base, "target"), filepath.Join(base, "outside")
	for _, d := range []string{target, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(target, "esc")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "esc/pwn.txt", Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	if _, err := Extract(root, &buf); err == nil {
		t.Error("Extract wrote esc/pwn.txt through a symlink to outside")
	}

	// Below the entry rules: os.Root itself refuses the escape.
	if _, err := writeEntry(root, &tar.Header{Typeflag: tar.TypeReg, Name: "esc/pwn2.txt", Mode: 0o644, Size: 1}, strings.NewReader("x")); err == nil {
		t.Error("writeEntry wrote esc/pwn2.txt through a symlink to outside")
	}
	if err := root.MkdirAll("esc/d", 0o755); err == nil {
		t.Error("root.MkdirAll created esc/d through a symlink to outside")
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("the outside directory gained %v", ents)
	}
}
