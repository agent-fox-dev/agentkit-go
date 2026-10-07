package tools_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/tools"
)

// TS-04-59 (smoke): An embedder runs a verifier with stdin, a relative named
// log, head truncation and the default reduced environment.
func TestTS_04_59_VerifierWithStdinLogHeadAndReducedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh and unix file modes")
	}
	if _, _, err := tools.ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	tmp := t.TempDir()

	// Write verify.sh: copies stdin to stdout, prints the env var (or
	// "absent"), prints 4 KiB of report lines, and exits 2.
	script := `#!/bin/sh
cat
printf '%s' "${AGENTKIT_TEST_API_KEY:-absent}"
i=0
while [ $i -lt 100 ]; do
  printf 'report line %04d: aaaaaaaaaaaaaaaaaaaaaaaaaaaa\n' "$i"
  i=$((i + 1))
done
exit 2
`
	if err := os.WriteFile(filepath.Join(tmp, "verify.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Build a 64 KiB deterministic input.
	inputSize := 64 * 1024
	inputBuf := make([]byte, inputSize)
	for i := range inputBuf {
		inputBuf[i] = byte('A' + (i % 26))
	}

	// Set the credential variable in the test process so ReducedEnv strips it.
	t.Setenv("AGENTKIT_TEST_API_KEY", "sekrit")

	res, err := tools.RunArgv(context.Background(), []string{"sh", "verify.sh"}, tools.ExecOptions{
		Dir:      tmp,
		Stdin:    bytes.NewReader(inputBuf),
		LogPath:  "logs/verify.log",
		KeepHead: true,
		MaxBytes: 1024,
		Timeout:  30 * time.Second,
		// Env is nil → ReducedEnv(nil)
	})
	if err != nil {
		t.Fatalf("RunArgv error = %v, want nil", err)
	}

	// ---- Outcome assertions ----
	if res.Outcome != tools.OutcomeExit {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, tools.OutcomeExit)
	}
	if res.ExitCode != 2 {
		t.Fatalf("ExitCode = %d, want 2", res.ExitCode)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.IOErr != nil {
		t.Fatalf("IOErr = %v, want nil", res.IOErr)
	}

	// ---- SpillPath assertions ----
	wantSpill := filepath.Join(tmp, "logs", "verify.log")
	if res.SpillPath != wantSpill {
		t.Fatalf("SpillPath = %q, want %q", res.SpillPath, wantSpill)
	}

	// Check directory and file modes on unix.
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Join(tmp, "logs"))
		if err != nil {
			t.Fatalf("stat logs dir: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm&0o777 != 0o700 {
			t.Fatalf("logs dir mode = %o, want 0700", perm)
		}
		fileInfo, err := os.Stat(wantSpill)
		if err != nil {
			t.Fatalf("stat log file: %v", err)
		}
		if perm := fileInfo.Mode().Perm(); perm&0o777 != 0o600 {
			t.Fatalf("log file mode = %o, want 0600", perm)
		}
	}

	// ---- Log file content assertions ----
	logData, err := os.ReadFile(wantSpill)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	// The log file should hold the complete output.
	if int64(len(logData)) != res.TotalBytes {
		t.Fatalf("log file size = %d, TotalBytes = %d, want equal", len(logData), res.TotalBytes)
	}

	// The log should start with the input bytes (stdin was cat'd to stdout).
	if !bytes.HasPrefix(logData, inputBuf) {
		t.Fatal("log file does not start with the input bytes")
	}

	// The credential variable should have been stripped by ReducedEnv.
	if bytes.Contains(logData, []byte("sekrit")) {
		t.Fatal("log file contains the credential value 'sekrit'; ReducedEnv should have stripped AGENTKIT_TEST_API_KEY")
	}
	if !bytes.Contains(logData, []byte("absent")) {
		t.Fatal("log file does not contain 'absent'; the env var should have been absent")
	}

	// ---- Output (head-truncated) assertions ----
	// Output should be the first 1024 bytes of the log, followed by the
	// head-mode marker.
	if len(res.Output) <= 1024 {
		t.Fatalf("Output length = %d, want > 1024 (should include marker)", len(res.Output))
	}
	prefix := res.Output[:1024]
	if prefix != string(logData[:1024]) {
		t.Fatal("Output[:1024] does not match log[:1024]")
	}

	elided := res.TotalBytes - 1024
	wantMarker := fmt.Sprintf("\n[%d bytes elided of %d total. Full output: %s]",
		elided, res.TotalBytes, wantSpill)
	if !strings.HasSuffix(res.Output, wantMarker) {
		t.Fatalf("Output suffix = %q, want %q", res.Output[1024:], wantMarker)
	}
}

// TS-04-60 (smoke): Timeout, cancellation and a short caller deadline each
// kill the process tree and are told apart through RunArgv.
func TestTS_04_60_TimeoutCancelDeadlineKillTreeAndDistinguish(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix process groups")
	}
	if _, _, err := tools.ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	type scenario struct {
		name        string
		timeout     time.Duration
		ctxFunc     func() (context.Context, context.CancelFunc)
		wantOutcome tools.Outcome
	}

	scenarios := []scenario{
		{
			name:    "timeout_200ms",
			timeout: 200 * time.Millisecond,
			ctxFunc: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			wantOutcome: tools.OutcomeTimeout,
		},
		{
			name: "cancel_after_200ms",
			ctxFunc: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(200*time.Millisecond, cancel)
				return ctx, cancel
			},
			wantOutcome: tools.OutcomeAbort,
		},
		{
			name:    "caller_deadline_200ms_timeout_5s",
			timeout: 5 * time.Second,
			ctxFunc: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 200*time.Millisecond)
			},
			wantOutcome: tools.OutcomeAbort,
		},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "grandchild-survived")

			ctx, cancel := sc.ctxFunc()
			defer cancel()

			// The command starts a background grandchild that would create a
			// marker file after 1s, then sleeps for 5s.
			cmd := []string{"sh", "-c",
				fmt.Sprintf(`( sleep 1; touch "%s" ) & sleep 5`, marker),
			}

			res, err := tools.RunArgv(ctx, cmd, tools.ExecOptions{
				Dir:     dir,
				Timeout: sc.timeout,
			})
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if res.Outcome != sc.wantOutcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, sc.wantOutcome)
			}
			if res.Duration >= 1*time.Second {
				t.Fatalf("Duration = %v, want < 1s", res.Duration)
			}
			// For the timeout scenario, Duration should be at least 200ms.
			if sc.wantOutcome == tools.OutcomeTimeout && res.Duration < 200*time.Millisecond {
				t.Fatalf("Duration = %v, want >= 200ms for timeout", res.Duration)
			}

			// Wait 1.5s past when the grandchild would have written.
			time.Sleep(1500 * time.Millisecond)
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the grandchild survived the kill and wrote its marker")
			}
		})
	}
}
