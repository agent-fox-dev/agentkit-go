package tools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TS-04-1: A nil Stdin gives the child the null device, so a command that
// reads stdin sees end-of-file at once.
func TestTS_04_1_NilStdinGivesNullDevice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	ctx := context.Background()

	// Sub-test: RunArgv
	t.Run("RunArgv", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sh", "-c", "cat; echo done"}, ExecOptions{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("unexpected IOErr: %v", res.IOErr)
		}
		if res.Output != "done\n" {
			t.Fatalf("Output = %q, want %q", res.Output, "done\n")
		}
		if res.Outcome != OutcomeOK {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
		}
		if res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
		}
		if res.Duration >= 1*time.Second {
			t.Fatalf("Duration = %v, want < 1s (cat should not block)", res.Duration)
		}
	})

	// Sub-test: Run
	t.Run("Run", func(t *testing.T) {
		res, err := Run(ctx, "cat; echo done", ExecOptions{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("unexpected IOErr: %v", res.IOErr)
		}
		if res.Output != "done\n" {
			t.Fatalf("Output = %q, want %q", res.Output, "done\n")
		}
		if res.Outcome != OutcomeOK {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
		}
		if res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
		}
		if res.Duration >= 1*time.Second {
			t.Fatalf("Duration = %v, want < 1s (cat should not block)", res.Duration)
		}
	})
}

// TS-04-2: A non-nil Stdin is copied into the child and closed at io.EOF,
// so cat echoes it byte for byte.
func TestTS_04_2_StdinCopiedToChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses cat")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	// 256 KiB of deterministic bytes including newlines and NUL bytes.
	input := make([]byte, 256*1024)
	for i := range input {
		input[i] = byte(i % 256)
	}

	ctx := context.Background()

	// Sub-test: RunArgv
	t.Run("RunArgv", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
			Stdin:    bytes.NewReader(input),
			MaxBytes: 1 << 20,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("unexpected IOErr: %v", res.IOErr)
		}
		if res.Output != string(input) {
			t.Fatalf("Output length = %d, want %d; bytes differ", len(res.Output), len(input))
		}
		if res.Truncated {
			t.Fatal("Truncated should be false")
		}
		if res.TotalBytes != int64(len(input)) {
			t.Fatalf("TotalBytes = %d, want %d", res.TotalBytes, len(input))
		}
		if res.Outcome != OutcomeOK {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
		}
	})

	// Sub-test: Run
	t.Run("Run", func(t *testing.T) {
		res, err := Run(ctx, "cat", ExecOptions{
			Stdin:    bytes.NewReader(input),
			MaxBytes: 1 << 20,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("unexpected IOErr: %v", res.IOErr)
		}
		if res.Output != string(input) {
			t.Fatalf("Output length = %d, want %d; bytes differ", len(res.Output), len(input))
		}
		if res.Truncated {
			t.Fatal("Truncated should be false")
		}
		if res.TotalBytes != int64(len(input)) {
			t.Fatalf("TotalBytes = %d, want %d", res.TotalBytes, len(input))
		}
		if res.Outcome != OutcomeOK {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
		}
	})
}

// TS-04-3: A child that exits or closes stdin without reading all input is
// not an error.
func TestTS_04_3_ChildExitsWithoutReadingAllInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	ctx := context.Background()
	bigReader := io.LimitReader(zeroReader{}, 16<<20) // 16 MiB of zeros

	cases := []struct {
		name string
		argv []string
	}{
		{"true", []string{"true"}},
		{"head", []string{"head", "-c", "1"}},
		{"close_stdin", []string{"sh", "-c", "exec 0<&-; echo closed"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := RunArgv(ctx, tc.argv, ExecOptions{
				Stdin:   io.LimitReader(zeroReader{}, 16<<20),
				Timeout: 10 * time.Second,
			})
			_ = bigReader // keep the outer variable alive for clarity
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IOErr != nil {
				t.Fatalf("unexpected IOErr: %v", res.IOErr)
			}
			if res.Outcome != OutcomeOK {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
			}
			if res.ExitCode != 0 {
				t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
			}
			if res.Duration >= 5*time.Second {
				t.Fatalf("Duration = %v, want < 5s", res.Duration)
			}
		})
	}
}

// zeroReader is an io.Reader that returns zero bytes forever.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

// TS-04-4: A Stdin read error closes the child's stdin and is reported in
// IOErr whatever the exit status.
func TestTS_04_4_StdinReadErrorReportedInIOErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	readerErr := errors.New("source failed")

	cases := []struct {
		name        string
		argv        []string
		wantOutcome Outcome
		wantCode    int
	}{
		{"exit0", []string{"sh", "-c", "cat; exit 0"}, OutcomeOK, 0},
		{"exit3", []string{"sh", "-c", "cat; exit 3"}, OutcomeExit, 3},
	}

	ctx := context.Background()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &failingReader{data: []byte("partial\n"), err: readerErr}
			res, err := RunArgv(ctx, tc.argv, ExecOptions{
				Stdin:   fr,
				Timeout: 5 * time.Second,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !errors.Is(res.IOErr, readerErr) {
				t.Fatalf("IOErr = %v, want errors.Is(_, readerErr)", res.IOErr)
			}
			if res.Outcome != tc.wantOutcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tc.wantOutcome)
			}
			if res.ExitCode != tc.wantCode {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, tc.wantCode)
			}
			if res.Output != "partial\n" {
				t.Fatalf("Output = %q, want %q", res.Output, "partial\n")
			}
		})
	}
}

// failingReader yields data once, then returns err on the next Read.
type failingReader struct {
	data []byte
	err  error
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		r.done = true
	}
	return n, nil
}

// TS-04-5: The call does not wait for a Stdin reader still blocked after the
// child exits, and the copier ends once the reader returns.
func TestTS_04_5_BlockedStdinReaderDoesNotBlockReturn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses true")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	pr, pw := io.Pipe()
	defer pw.Close()

	wrapped := &recordingReader{r: pr}

	baseline := runtime.NumGoroutine()

	start := time.Now()
	res, err := RunArgv(context.Background(), []string{"true"}, ExecOptions{
		Stdin:     wrapped,
		DrainIdle: 100 * time.Millisecond,
		Timeout:   10 * time.Second,
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeOK {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("call took %v, want < 2s (should not wait for blocked reader)", elapsed)
	}

	// Unblock the reader.
	pw.CloseWithError(errors.New("late"))

	// Wait for the copier goroutine to end.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if runtime.NumGoroutine() > baseline+2 {
		t.Fatalf("goroutine count = %d, want <= %d (copier should have ended)", runtime.NumGoroutine(), baseline)
	}

	// No further reads after the pipe was closed.
	readsAfterClose := wrapped.readCount()
	time.Sleep(200 * time.Millisecond)
	if wrapped.readCount() != readsAfterClose {
		t.Fatal("reader was called after the copier should have ended")
	}
}

// recordingReader wraps an io.Reader and counts Read calls.
type recordingReader struct {
	r     io.Reader
	mu    sync.Mutex
	count int64
}

func (r *recordingReader) Read(p []byte) (int, error) {
	atomic.AddInt64(&r.count, 1)
	return r.r.Read(p)
}

func (r *recordingReader) readCount() int64 {
	return atomic.LoadInt64(&r.count)
}
