//go:build unix

package tarsafe

import (
	"bytes"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestArchiveSkipsEntryTypesOtherThanFilesDirectoriesAndLinks(t *testing.T) {
	src := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(src, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Archive(openRoot(t, src), ".", &buf); err != nil {
		t.Fatal(err)
	}
	if got := names(t, buf.Bytes()); !slices.Equal(got, nil) {
		t.Errorf("Archive holds %v, want nothing", got)
	}
}
