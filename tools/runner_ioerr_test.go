package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TS-04-15: A log or spill write failure after start stops that file, leaves
// the process running and is reported in IOErr.
func TestTS_04_15_WriteFailureReportedInIOErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	writeErr := errors.New("disk full sentinel")

	// failAfterFirstWriter succeeds on the first Write, fails with writeErr
	// on the second, and counts any Write calls after the failure.
	type failAfterFirstWriter struct {
		calls              atomic.Int64
		writesAfterFailure atomic.Int64
	}
	// Wrap as io.Writer.
	wrapFail := func(fw *failAfterFirstWriter) writerFunc {
		return func(p []byte) (int, error) {
			n := fw.calls.Add(1)
			if n == 1 {
				// First write: succeed.
				return len(p), nil
			}
			if n == 2 {
				// Second write: fail with writeErr.
				return 0, writeErr
			}
			// Any write after the failure: count it. The accumulator
			// should never reach here because spillErr stops writes.
			fw.writesAfterFailure.Add(1)
			return 0, writeErr
		}
	}

	ctx := context.Background()

	t.Run("log", func(t *testing.T) {
		tmp := t.TempDir()
		logFile := filepath.Join(tmp, "run.log")

		fw := &failAfterFirstWriter{}
		old := wrapExecFile
		wrapExecFile = func(w *os.File) fileWriter {
			return writerFunc(wrapFail(fw))
		}
		t.Cleanup(func() { wrapExecFile = old })

		res, err := RunArgv(ctx, []string{"sh", "-c", "printf first; sleep 0.1; printf second; exit 3"}, ExecOptions{
			LogPath:  logFile,
			MaxBytes: 4096,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !errors.Is(res.IOErr, writeErr) {
			t.Fatalf("IOErr = %v, want errors.Is(_, writeErr)", res.IOErr)
		}
		if res.Outcome != OutcomeExit {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeExit)
		}
		if res.ExitCode != 3 {
			t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
		}
		if !strings.Contains(res.Output, "first") || !strings.Contains(res.Output, "second") {
			t.Fatalf("Output = %q, want it to contain 'first' and 'second'", res.Output)
		}
		if fw.writesAfterFailure.Load() != 0 {
			t.Fatalf("writesAfterFailure = %d, want 0", fw.writesAfterFailure.Load())
		}
	})

	t.Run("spill", func(t *testing.T) {
		tmp := t.TempDir()
		spillDir := filepath.Join(tmp, "spill")

		fw := &failAfterFirstWriter{}
		old := wrapExecFile
		wrapExecFile = func(w *os.File) fileWriter {
			return writerFunc(wrapFail(fw))
		}
		t.Cleanup(func() { wrapExecFile = old })

		res, err := RunArgv(ctx, []string{"sh", "-c", "printf first; sleep 0.1; printf second; exit 3"}, ExecOptions{
			SpillDir: spillDir,
			MaxBytes: 4096,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !errors.Is(res.IOErr, writeErr) {
			t.Fatalf("IOErr = %v, want errors.Is(_, writeErr)", res.IOErr)
		}
		if res.Outcome != OutcomeExit {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeExit)
		}
		if res.ExitCode != 3 {
			t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
		}
		if !strings.Contains(res.Output, "first") || !strings.Contains(res.Output, "second") {
			t.Fatalf("Output = %q, want it to contain 'first' and 'second'", res.Output)
		}
		if fw.writesAfterFailure.Load() != 0 {
			t.Fatalf("writesAfterFailure = %d, want 0", fw.writesAfterFailure.Load())
		}
	})

	// Linux-only: /dev/full test.
	if runtime.GOOS == "linux" {
		t.Run("devfull", func(t *testing.T) {
			// /dev/full writes fail with ENOSPC.
			res, err := RunArgv(ctx, []string{"sh", "-c", "printf first; sleep 0.1; printf second; exit 3"}, ExecOptions{
				LogPath:  "/dev/full",
				MaxBytes: 4096,
				Timeout:  10 * time.Second,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.IOErr == nil {
				t.Fatal("IOErr is nil, want non-nil (ENOSPC)")
			}
			// On Linux /dev/full is a character device, not a regular file.
			// The open may succeed but writes fail with ENOSPC.
			// Check that the process still ran to completion.
			if res.Outcome != OutcomeExit {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeExit)
			}
			if res.ExitCode != 3 {
				t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
			}
		})
	}
}

// writerFunc adapts a function to the fileWriter interface.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
func (f writerFunc) Close() error                { return nil }

// TS-04-27: A Wait error that is neither an exit status nor a signal lands in
// IOErr, and IOErr is nil on every clean call.
func TestTS_04_27_WaitErrorLandsInIOErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	ctx := context.Background()

	// With the seam: inject exec.ErrWaitDelay after the real Wait.
	t.Run("seam_wait_error", func(t *testing.T) {
		old := waitCmd
		waitCmd = func(c *exec.Cmd) error {
			_ = c.Wait() // real wait
			return exec.ErrWaitDelay
		}
		t.Cleanup(func() { waitCmd = old })

		res, err := RunArgv(ctx, []string{"sh", "-c", "exit 0"}, ExecOptions{
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !errors.Is(res.IOErr, exec.ErrWaitDelay) {
			t.Fatalf("IOErr = %v, want errors.Is(_, exec.ErrWaitDelay)", res.IOErr)
		}
		if res.Outcome != OutcomeOK {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeOK)
		}
		if res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0", res.ExitCode)
		}
	})

	// Clean calls: IOErr must be nil on each.
	t.Run("clean_exit0", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sh", "-c", "exit 0"}, ExecOptions{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
	})

	t.Run("clean_exit3", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sh", "-c", "exit 3"}, ExecOptions{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
	})

	t.Run("clean_timeout", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sleep", "10"}, ExecOptions{Timeout: 200 * time.Millisecond})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
		if res.Outcome != OutcomeTimeout {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeTimeout)
		}
	})

	t.Run("clean_cancel", func(t *testing.T) {
		cancelCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		res, err := RunArgv(cancelCtx, []string{"sleep", "10"}, ExecOptions{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
		if res.Outcome != OutcomeAbort {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeAbort)
		}
	})

	t.Run("clean_stdin", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
			Stdin:   bytes.NewReader([]byte("hello")),
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
		if res.Output != "hello" {
			t.Fatalf("Output = %q, want %q", res.Output, "hello")
		}
	})

	t.Run("clean_logpath", func(t *testing.T) {
		tmp := t.TempDir()
		logFile := filepath.Join(tmp, "run.log")
		res, err := RunArgv(ctx, []string{"sh", "-c", fmt.Sprintf("echo ok")}, ExecOptions{
			LogPath: logFile,
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.IOErr != nil {
			t.Fatalf("IOErr = %v, want nil", res.IOErr)
		}
		got, _ := os.ReadFile(logFile)
		if string(got) != "ok\n" {
			t.Fatalf("log = %q, want %q", string(got), "ok\n")
		}
	})
}
