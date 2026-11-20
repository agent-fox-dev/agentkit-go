package output

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
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
	testutil.WriteTree(t, filepath.Dir(blocker), map[string]string{"file": "x"})
	bad := NewAccumulator(4, TruncateTail)
	bad.SpillDir = filepath.Join(blocker, "sub")
	_, _ = bad.Write([]byte("0123456789"))
	if bad.FileError() == nil || bad.String() != "[6 bytes elided of 10 total.]\n6789" {
		t.Fatalf("FileError %v, String %q", bad.FileError(), bad.String())
	}
	if bad.SpillPath() != "" {
		t.Fatalf("a spill that never opened has a path: %q", bad.SpillPath())
	}
}

// failAfter is a writer whose first write succeeds and whose later ones fail.
type failAfter struct {
	err    error
	writes int
	closed bool
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.writes++; f.writes == 1 {
		return len(p), nil
	}
	return 0, f.err
}
func (f *failAfter) Close() error { f.closed = true; return nil }

// A destination that stops taking writes costs the complete copy and nothing
// else: the window is kept, the first error is held, and no further write
// reaches the file.
func TestTheAccumulatorKeepsItsWindowWhenItsFileStopsTakingWrites(t *testing.T) {
	sentinel := errors.New("disk full")
	for _, dest := range []string{"a spill file it created", "a file it was handed"} {
		t.Run(dest, func(t *testing.T) {
			ff := &failAfter{err: sentinel}
			a := NewAccumulator(4, TruncateTail)
			if dest == "a file it was handed" {
				a.UseFile(ff, "/somewhere/run.log")
				if a.SpillPath() != "/somewhere/run.log" {
					t.Fatalf("the path of a handed file is known from the start, got %q", a.SpillPath())
				}
			} else {
				orig := wrapSpill
				wrapSpill = func(*os.File) io.Writer { return ff }
				t.Cleanup(func() { wrapSpill = orig })
				a.SpillDir, a.SpillPrefix = t.TempDir(), "p"
			}
			if a.FileError() != nil {
				t.Fatalf("FileError before any write: %v", a.FileError())
			}
			for _, s := range []string{"0123", "4567", "89"} {
				if n, err := a.Write([]byte(s)); n != len(s) || err != nil {
					t.Fatalf("Write(%q) = %d, %v; a file failure must not fail the write", s, n, err)
				}
			}
			if !errors.Is(a.FileError(), sentinel) || ff.writes != 2 {
				t.Fatalf("FileError %v after %d writes to the file; want the first error and no write after it", a.FileError(), ff.writes)
			}
			want := "[6 bytes elided of 10 total. Full output: " + a.SpillPath() + "]\n6789"
			if a.Total() != 10 || a.String() != want {
				t.Fatalf("Total %d, String %q, want %q", a.Total(), a.String(), want)
			}
			if err := a.Close(); err != nil || (dest == "a file it was handed" && !ff.closed) {
				t.Fatalf("Close = %v, closed %v", err, ff.closed)
			}
		})
	}
}
