//go:build !windows

package tools

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// 04-REQ-5.3 (TS-04-22): a timed-out call's Duration is at least its Timeout.
// The time spent before the child starts, here a log file whose open blocks for
// 100ms, is inside the Timeout's budget, so it must be inside the Duration the
// call reports too; a Duration that starts after the setup comes up short.
func TestTS_04_22_DurationCoversTimeoutAfterSlowSetup(t *testing.T) {
	const (
		setup   = 100 * time.Millisecond
		timeout = 200 * time.Millisecond
	)
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	if err := syscall.Mkfifo(log, 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}

	// Opening a named pipe for writing waits for a reader, so RunArgv stays in
	// its setup until this one opens it. It reads to EOF, which RunArgv's
	// closing of the log delivers.
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		time.Sleep(setup)
		f, err := os.Open(log)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = io.Copy(io.Discard, f)
	}()

	res, err := RunArgv(context.Background(), []string{"sleep", "5"}, ExecOptions{
		Dir:     dir,
		LogPath: log,
		Timeout: timeout,
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	<-readerDone
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeTimeout)
	}
	if res.Duration < timeout {
		t.Fatalf("Duration = %v, want >= Timeout %v (setup took %v of it)", res.Duration, timeout, setup)
	}
	if res.Duration >= 1*time.Second {
		t.Fatalf("Duration = %v, want < 1s", res.Duration)
	}
}
