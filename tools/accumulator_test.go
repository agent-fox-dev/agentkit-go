package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheAccumulatorKeepsTheEndItsModeNames(t *testing.T) {
	const s = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
	for _, c := range []struct {
		name string
		mode TruncateMode
		want string
	}{
		{"tail keeps the end, where a build puts its error", TruncateTail, "[90 bytes elided of 100 total.]\n" + s[90:]},
		{"head keeps the beginning", TruncateHead, s[:10] + "\n[90 bytes elided of 100 total.]"},
		{"middle keeps both ends", TruncateMiddle, s[:5] + "\n[90 bytes elided of 100 total.]\n" + s[95:]},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := NewAccumulator(10, c.mode)
			// Written in pieces, so the retained window is assembled across writes.
			for i := 0; i < len(s); i += 7 {
				_, _ = a.Write([]byte(s[i:min(i+7, len(s))]))
			}
			if got := a.String(); got != c.want || !a.Truncated() || a.Total() != 100 {
				t.Fatalf("String = %q (truncated %v, total %d), want %q", got, a.Truncated(), a.Total(), c.want)
			}
		})
	}
	whole := NewAccumulator(0, TruncateMiddle) // 0 means DefaultByteLimit
	_, _ = whole.Write([]byte("short"))
	if whole.Cap != DefaultByteLimit || whole.String() != "short" || whole.Truncated() || whole.SpillPath() != "" {
		t.Fatalf("an untruncated stream is returned whole: %q", whole.String())
	}
}

func TestTheAccumulatorIsBoundedRegardlessOfVolume(t *testing.T) {
	a := NewAccumulator(1024, TruncateTail)
	chunk := []byte(strings.Repeat("x", 4096))
	for i := 0; i < 2000; i++ { // 8 MB
		_, _ = a.Write(chunk)
	}
	if got := len(a.head) + len(a.tail); got > 2*1024 || a.Total() != 2000*4096 {
		t.Fatalf("retained %d bytes of %d written, for a cap of 1024", got, a.Total())
	}
}

func TestTheAccumulatorSpillsTheCompleteOutputLazily(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spill")
	a := NewAccumulator(4, TruncateTail)
	a.SpillDir, a.SpillPrefix = dir, "p"
	if _, err := os.Stat(dir); err == nil || a.SpillPath() != "" {
		t.Fatal("nothing written yet, but the spill directory exists")
	}
	_, _ = a.Write([]byte("0123456789"))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(a.SpillPath())
	if string(b) != "0123456789" || err != nil || !strings.HasPrefix(filepath.Base(a.SpillPath()), "p-") {
		t.Fatalf("spill %s holds %q, %v", a.SpillPath(), b, err)
	}
	if !strings.Contains(a.String(), "Full output: "+a.SpillPath()) || a.Close() != nil {
		t.Fatalf("the marker does not name the spill file: %q", a.String())
	}

	// A spill directory that cannot be made is recorded, and the window kept.
	blocker := filepath.Join(t.TempDir(), "file")
	writeAt(t, filepath.Dir(blocker), "file", "x")
	bad := NewAccumulator(4, TruncateTail)
	bad.SpillDir = filepath.Join(blocker, "sub")
	_, _ = bad.Write([]byte("0123456789"))
	if bad.fileError() == nil || bad.String() != "[6 bytes elided of 10 total.]\n6789" {
		t.Fatalf("fileError %v, String %q", bad.fileError(), bad.String())
	}
}
