package tarsafe

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractWritesEntriesAndCountsTheFiles(t *testing.T) {
	root, dir, _ := target(t)
	st, err := Extract(root, bytes.NewReader(tarOf(t,
		folder("d/"), reg("d/f", "hello", 0o4755), reg("deep/er/g", "abc", 0o644), symlink("l", "d/f"))))
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stats{Files: 2, Bytes: 8}) {
		t.Errorf("Stats = %+v, want 2 files of 8 bytes", st)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "l")); string(b) != "hello" {
		t.Errorf("l reads %q through its link", b)
	}
	fi, err := os.Stat(filepath.Join(dir, "d", "f"))
	if err != nil || fi.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Errorf("d/f has mode %v, %v; want the special bits cleared", fi.Mode(), err)
	}
}

func TestExtractStopsAtTheFirstRefusedEntryKeepingTheEarlierOnes(t *testing.T) {
	root, dir, _ := target(t)
	_, err := Extract(root, bytes.NewReader(tarOf(t,
		reg("before", "x", 0o644), symlink("esc", "../outside"), reg("after", "x", 0o644))))
	var ee *EntryError
	if !errors.As(err, &ee) || ee.Name != "esc" {
		t.Fatalf("Extract = %v, want an EntryError naming esc", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "before")); err != nil {
		t.Errorf("the entry before the refused one is gone: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "after")); err == nil {
		t.Error("the entry after the refused one was written")
	}
}

// Writes go through os.Root, so a symlink already on disk cannot carry a
// write out of the target, under the entry rules or below them.
func TestExtractNeverWritesThroughAnEscapingSymlinkOnDisk(t *testing.T) {
	root, dir, outside := target(t)
	if err := os.Symlink(outside, filepath.Join(dir, "esc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(root, bytes.NewReader(tarOf(t, reg("esc/pwn", "x", 0o644)))); err == nil {
		t.Error("Extract wrote esc/pwn through a symlink to outside")
	}
	h := reg("esc/pwn2", "x", 0o644).hdr
	if _, err := writeEntry(root, &h, strings.NewReader("x")); err == nil {
		t.Error("writeEntry wrote esc/pwn2 through a symlink to outside")
	}
}

func TestExtractReplacesASymlinkOrAFileRatherThanWritingThroughIt(t *testing.T) {
	root, dir, _ := target(t)
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(root, bytes.NewReader(tarOf(t, reg("link", "new", 0o644), symlink("file", "real")))); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "real")); string(b) != "keep" {
		t.Errorf("real = %q; the write went through the link", b)
	}
	if fi, _ := os.Lstat(filepath.Join(dir, "link")); !fi.Mode().IsRegular() {
		t.Errorf("link is %v, want a regular file", fi.Mode())
	}
	if l, err := os.Readlink(filepath.Join(dir, "file")); err != nil || l != "real" {
		t.Errorf("file -> %q, %v; want a link to real", l, err)
	}
}

func TestExtractReportsAnEntryItCannotWrite(t *testing.T) {
	root, dir, _ := target(t)
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Extract(root, bytes.NewReader(tarOf(t, reg("d", "x", 0o644))))
	var ee *EntryError
	if !errors.As(err, &ee) || ee.Name != "d" {
		t.Errorf("Extract over a directory = %v, want an EntryError naming d", err)
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// A failing source is reported as itself; a cut archive is a malformed one
// that names the last header read.
func TestExtractTellsASourceFailureFromAMalformedArchive(t *testing.T) {
	blob := tarOf(t, reg("a", strings.Repeat("x", 2000), 0o644), reg("b", "y", 0o644))
	boom := errors.New("boom")
	for _, c := range []struct {
		name string
		r    io.Reader
		want func(error) bool
	}{
		{"source fails mid-body", &failingReader{blob[:1000], boom}, func(err error) bool { return errors.Is(err, boom) }},
		{"source fails at a header", &failingReader{blob[:3072], boom}, func(err error) bool { return errors.Is(err, boom) }},
		{"cut mid-body", bytes.NewReader(blob[:1000]), func(err error) bool {
			var ee *EntryError
			return errors.As(err, &ee) && ee.Name == "a"
		}},
		{"garbage", strings.NewReader(strings.Repeat("\x01", 1024)), func(err error) bool {
			var ee *EntryError
			return errors.As(err, &ee) && ee.Name == "" && strings.Contains(err.Error(), "malformed")
		}},
	} {
		root, _, _ := target(t)
		if _, err := Extract(root, c.r); !c.want(err) {
			t.Errorf("%s: Extract = %v", c.name, err)
		}
	}
}

func TestExtractCreatesADirectoryWithOwnerAccess(t *testing.T) {
	root, dir, _ := target(t)
	h := tar.Header{Typeflag: tar.TypeDir, Name: "ro/", Mode: 0o500}
	if _, err := Extract(root, bytes.NewReader(tarOf(t, entry{hdr: h}, reg("ro/f", "x", 0o644)))); err != nil {
		t.Fatalf("Extract into a 0500 directory entry: %v", err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "ro")); fi.Mode().Perm() != 0o700 {
		t.Errorf("ro has mode %v, want 0700", fi.Mode().Perm())
	}
}
