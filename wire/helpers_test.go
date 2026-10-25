package wire_test

import (
	"errors"
	"io"
	"runtime"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/wire"
)

// wireErr asserts err is a *wire.Error that matches ErrRejected.
func wireErr(t *testing.T, err error) *wire.Error {
	t.Helper()
	var we *wire.Error
	if !errors.As(err, &we) {
		t.Fatalf("err = %v (%T), want a *wire.Error", err, err)
	}
	if !errors.Is(err, wire.ErrRejected) {
		t.Fatalf("%v does not match ErrRejected", err)
	}
	return we
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

// totalAlloc is cumulative bytes allocated: unlike HeapAlloc it still shows a
// buffer that is garbage by the time it is read.
func totalAlloc() int64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.TotalAlloc)
}

// newlines yields an endless stream of '\n' without holding it in memory.
type newlines struct{}

func (newlines) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = '\n'
	}
	return len(p), nil
}

// drain reads frames until the reader stops, asserting that a rejection
// poisons the reader with the same error and that a clean EOF does not.
func drain(t *testing.T, r *wire.FrameReader, b []byte) {
	t.Helper()
	for range 1 << 12 {
		_, err := r.Next()
		if err == nil {
			continue
		}
		if err == io.EOF {
			if r.Poisoned() {
				t.Fatalf("a clean EOF poisoned the reader on %q", b)
			}
			return
		}
		if !r.Poisoned() {
			t.Fatalf("Next returned %v on %q but the reader is not poisoned", err, b)
		}
		if !errors.Is(err, wire.ErrRejected) && err != io.ErrUnexpectedEOF {
			t.Fatalf("Next returned %v (%T) on %q; every rejection must match ErrRejected", err, err, b)
		}
		if _, again := r.Next(); again == nil || again.Error() != err.Error() {
			t.Fatalf("a poisoned reader resynchronized on %q: first %v, then %v", b, err, again)
		}
		return
	}
}
