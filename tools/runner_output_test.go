package tools

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TS-04-6: KeepHead false keeps the tail with the tail-mode marker, exactly
// as the Accumulator rendered it before.
func TestTS_04_6_KeepHeadFalseKeepsTail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses printf")
	}

	// 100-byte string of distinct printable characters.
	s := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
	if len(s) != 100 {
		t.Fatalf("test string length = %d, want 100", len(s))
	}

	ctx := context.Background()

	// Without SpillDir.
	t.Run("no_spill", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"printf", "%s", s}, ExecOptions{MaxBytes: 10, Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Build the expected output from the Accumulator directly.
		acc := NewAccumulator(10, TruncateTail)
		_, _ = acc.Write([]byte(s))
		accStr := acc.String()

		wantNoSpill := "[90 bytes elided of 100 total.]\n" + s[90:]
		if res.Output != wantNoSpill {
			t.Fatalf("Output = %q, want %q", res.Output, wantNoSpill)
		}
		if res.Output != accStr {
			t.Fatalf("Output differs from Accumulator.String(): got %q, acc %q", res.Output, accStr)
		}
		if !res.Truncated {
			t.Fatal("Truncated should be true")
		}
		if res.TotalBytes != 100 {
			t.Fatalf("TotalBytes = %d, want 100", res.TotalBytes)
		}
	})

	// With SpillDir.
	t.Run("with_spill", func(t *testing.T) {
		spill := t.TempDir()
		res, err := RunArgv(ctx, []string{"printf", "%s", s}, ExecOptions{
			MaxBytes: 10,
			SpillDir: spill,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantSpill := fmt.Sprintf("[90 bytes elided of 100 total. Full output: %s]\n%s", res.SpillPath, s[90:])
		if res.Output != wantSpill {
			t.Fatalf("Output = %q, want %q", res.Output, wantSpill)
		}
		if !res.Truncated {
			t.Fatal("Truncated should be true")
		}
		if res.TotalBytes != 100 {
			t.Fatalf("TotalBytes = %d, want 100", res.TotalBytes)
		}
	})
}

// TS-04-7: KeepHead true keeps the first MaxBytes bytes followed by the
// head-mode marker naming the spill or log path when one exists.
func TestTS_04_7_KeepHeadTrueKeepsHead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses printf")
	}

	s := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
	if len(s) != 100 {
		t.Fatalf("test string length = %d, want 100", len(s))
	}

	ctx := context.Background()

	// No file.
	t.Run("no_file", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"printf", "%s", s}, ExecOptions{
			MaxBytes: 10,
			KeepHead: true,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := s[:10] + "\n[90 bytes elided of 100 total.]"
		if res.Output != want {
			t.Fatalf("Output = %q, want %q", res.Output, want)
		}
		if !res.Truncated {
			t.Fatal("Truncated should be true")
		}
		if res.TotalBytes != 100 {
			t.Fatalf("TotalBytes = %d, want 100", res.TotalBytes)
		}
	})

	// With SpillDir.
	t.Run("with_spill", func(t *testing.T) {
		spill := t.TempDir()
		res, err := RunArgv(ctx, []string{"printf", "%s", s}, ExecOptions{
			MaxBytes: 10,
			KeepHead: true,
			SpillDir: spill,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := s[:10] + fmt.Sprintf("\n[90 bytes elided of 100 total. Full output: %s]", res.SpillPath)
		if res.Output != want {
			t.Fatalf("Output = %q, want %q", res.Output, want)
		}
		if !res.Truncated {
			t.Fatal("Truncated should be true")
		}
		if res.TotalBytes != 100 {
			t.Fatalf("TotalBytes = %d, want 100", res.TotalBytes)
		}
	})

	// With LogPath.
	t.Run("with_logpath", func(t *testing.T) {
		tmp := t.TempDir()
		logFile := filepath.Join(tmp, "run.log")
		res, err := RunArgv(ctx, []string{"printf", "%s", s}, ExecOptions{
			MaxBytes: 10,
			KeepHead: true,
			LogPath:  logFile,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		absLog, _ := filepath.Abs(logFile)
		if !strings.HasSuffix(res.Output, "Full output: "+absLog+"]") {
			t.Fatalf("Output = %q, want suffix 'Full output: %s]'", res.Output, absLog)
		}
		if !res.Truncated {
			t.Fatal("Truncated should be true")
		}
		if res.TotalBytes != 100 {
			t.Fatalf("TotalBytes = %d, want 100", res.TotalBytes)
		}
	})
}

// TS-04-8: MaxBytes <= 0 means DefaultByteLimit in both modes, and output of
// exactly MaxBytes bytes is returned whole with no marker.
func TestTS_04_8_MaxBytesDefaultAndExact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses cat")
	}

	ctx := context.Background()

	// Helper: generate n bytes of deterministic content.
	bytesN := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('A' + (i % 26))
		}
		return b
	}

	for _, head := range []bool{false, true} {
		for _, mb := range []int{0, -1} {
			name := fmt.Sprintf("head=%v_mb=%d", head, mb)

			// Exactly DefaultByteLimit bytes: not truncated.
			t.Run(name+"_exact", func(t *testing.T) {
				input := bytesN(DefaultByteLimit)
				res, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
					Stdin:    bytes.NewReader(input),
					MaxBytes: mb,
					KeepHead: head,
					Timeout:  10 * time.Second,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if res.Truncated {
					t.Fatal("Truncated should be false")
				}
				if len(res.Output) != DefaultByteLimit {
					t.Fatalf("Output length = %d, want %d", len(res.Output), DefaultByteLimit)
				}
				if res.TotalBytes != int64(DefaultByteLimit) {
					t.Fatalf("TotalBytes = %d, want %d", res.TotalBytes, DefaultByteLimit)
				}
			})

			// DefaultByteLimit+1 bytes: truncated.
			t.Run(name+"_plus1", func(t *testing.T) {
				input := bytesN(DefaultByteLimit + 1)
				res, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
					Stdin:    bytes.NewReader(input),
					MaxBytes: mb,
					KeepHead: head,
					Timeout:  10 * time.Second,
				})
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !res.Truncated {
					t.Fatal("Truncated should be true")
				}
				if res.TotalBytes != int64(DefaultByteLimit+1) {
					t.Fatalf("TotalBytes = %d, want %d", res.TotalBytes, DefaultByteLimit+1)
				}
				wantMarker := fmt.Sprintf("[1 bytes elided of %d total.]", DefaultByteLimit+1)
				if !strings.Contains(res.Output, wantMarker) {
					t.Fatalf("Output missing marker %q; got %q", wantMarker, res.Output[:min(200, len(res.Output))])
				}
			})
		}

		// Exactly MaxBytes (64) bytes: not truncated.
		t.Run(fmt.Sprintf("head=%v_exact64", head), func(t *testing.T) {
			input := bytesN(64)
			res, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
				Stdin:    bytes.NewReader(input),
				MaxBytes: 64,
				KeepHead: head,
				Timeout:  10 * time.Second,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Truncated {
				t.Fatal("Truncated should be false")
			}
			if res.Output != string(input) {
				t.Fatalf("Output = %q, want %q", res.Output, string(input))
			}
			if res.TotalBytes != 64 {
				t.Fatalf("TotalBytes = %d, want 64", res.TotalBytes)
			}
		})
	}
}

// TS-04-9: Head and tail mode report the same TotalBytes and Truncated for
// any output and any MaxBytes (property test).
func TestTS_04_9_HeadTailSameTotalAndTruncated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses cat")
	}

	ctx := context.Background()
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < 40; i++ {
		mb := rng.Intn(256) + 1 // 1..256
		seqLen := rng.Intn(4*mb + 1)
		seq := make([]byte, seqLen)
		for j := range seq {
			seq[j] = byte(rng.Intn(256))
		}

		tailRes, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
			Stdin:    bytes.NewReader(seq),
			MaxBytes: mb,
			KeepHead: false,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("case %d tail: unexpected error: %v", i, err)
		}

		headRes, err := RunArgv(ctx, []string{"cat"}, ExecOptions{
			Stdin:    bytes.NewReader(seq),
			MaxBytes: mb,
			KeepHead: true,
			Timeout:  10 * time.Second,
		})
		if err != nil {
			t.Fatalf("case %d head: unexpected error: %v", i, err)
		}

		if tailRes.TotalBytes != headRes.TotalBytes {
			t.Fatalf("case %d: TotalBytes tail=%d head=%d", i, tailRes.TotalBytes, headRes.TotalBytes)
		}
		if tailRes.TotalBytes != int64(seqLen) {
			t.Fatalf("case %d: TotalBytes = %d, want %d", i, tailRes.TotalBytes, seqLen)
		}
		if tailRes.Truncated != headRes.Truncated {
			t.Fatalf("case %d: Truncated tail=%v head=%v", i, tailRes.Truncated, headRes.Truncated)
		}
		wantTrunc := seqLen > mb
		if tailRes.Truncated != wantTrunc {
			t.Fatalf("case %d: Truncated = %v, want %v (seqLen=%d, mb=%d)", i, tailRes.Truncated, wantTrunc, seqLen, mb)
		}
	}
}

// TS-04-10: LogPath receives the complete interleaved output even when Output
// is truncated.
func TestTS_04_10_LogPathReceivesCompleteOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}

	ctx := context.Background()
	tmp := t.TempDir()

	// Command that writes about 200 KiB alternating stdout and stderr lines.
	cmd := `for i in $(seq 1 4000); do echo out$i; echo err$i >&2; done`

	// Build expected output.
	var want strings.Builder
	for i := 1; i <= 4000; i++ {
		fmt.Fprintf(&want, "out%d\nerr%d\n", i, i)
	}
	wantStr := want.String()

	for _, head := range []bool{false, true} {
		name := fmt.Sprintf("head=%v", head)
		t.Run(name, func(t *testing.T) {
			logFile := filepath.Join(tmp, fmt.Sprintf("run_%v.log", head))
			res, err := RunArgv(ctx, []string{"sh", "-c", cmd}, ExecOptions{
				LogPath:  logFile,
				MaxBytes: 1024,
				KeepHead: head,
				Timeout:  30 * time.Second,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, readErr := os.ReadFile(logFile)
			if readErr != nil {
				t.Fatalf("reading log: %v", readErr)
			}
			if string(got) != wantStr {
				t.Fatalf("log content differs: got %d bytes, want %d bytes", len(got), len(wantStr))
			}
			if int64(len(got)) != res.TotalBytes {
				t.Fatalf("log size = %d, TotalBytes = %d", len(got), res.TotalBytes)
			}
			if !res.Truncated {
				t.Fatal("Truncated should be true")
			}
		})
	}

	// Verify the two logs are byte-identical.
	log1, _ := os.ReadFile(filepath.Join(tmp, "run_false.log"))
	log2, _ := os.ReadFile(filepath.Join(tmp, "run_true.log"))
	if !bytes.Equal(log1, log2) {
		t.Fatal("tail-mode and head-mode logs differ")
	}
}

// TS-04-11: A relative LogPath resolves against Dir, or against the process
// working directory when Dir is empty.
func TestTS_04_11_RelativeLogPathResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses true")
	}

	ctx := context.Background()
	tmpA := t.TempDir()
	tmpB := t.TempDir()

	// With Dir set.
	t.Run("with_dir", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"true"}, ExecOptions{
			Dir:     tmpA,
			LogPath: "logs/run.log",
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantPath := filepath.Join(tmpA, "logs", "run.log")
		if !filepath.IsAbs(res.SpillPath) {
			t.Fatalf("SpillPath = %q, want absolute", res.SpillPath)
		}
		if res.SpillPath != wantPath {
			t.Fatalf("SpillPath = %q, want %q", res.SpillPath, wantPath)
		}
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("log file does not exist: %v", err)
		}
	})

	// Without Dir, using process working directory.
	t.Run("no_dir", func(t *testing.T) {
		// t.Chdir changes the process working directory for the test.
		t.Chdir(tmpB)

		res, err := RunArgv(ctx, []string{"true"}, ExecOptions{
			LogPath: "run.log",
			Timeout: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !filepath.IsAbs(res.SpillPath) {
			t.Fatalf("SpillPath = %q, want absolute", res.SpillPath)
		}
		// Resolve symlinks for comparison (macOS /tmp -> /private/tmp).
		gotReal, _ := filepath.EvalSymlinks(res.SpillPath)
		wantReal, _ := filepath.EvalSymlinks(filepath.Join(tmpB, "run.log"))
		if gotReal != wantReal {
			t.Fatalf("SpillPath (resolved) = %q, want %q", gotReal, wantReal)
		}
		if _, err := os.Stat(res.SpillPath); err != nil {
			t.Fatalf("log file does not exist: %v", err)
		}
	})
}

// TS-04-12: Missing log parents are created 0o700 and the log file 0o600
// before the process starts, truncating an existing file.
func TestTS_04_12_LogPathPermissionsAndTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions test")
	}

	ctx := context.Background()
	tmp := t.TempDir()

	// Deep nested path.
	logPath := filepath.Join(tmp, "a", "b", "c", "run.log")
	res, err := RunArgv(ctx, []string{"sh", "-c", fmt.Sprintf(`test -f "%s" && echo present`, logPath)}, ExecOptions{
		LogPath: logPath,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Output != "present\n" {
		t.Fatalf("Output = %q, want %q (log should exist before child starts)", res.Output, "present\n")
	}

	// Check directory permissions.
	for _, d := range []string{"a", "a/b", "a/b/c"} {
		info, err := os.Stat(filepath.Join(tmp, d))
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir %s perm = %o, want 0700", d, perm)
		}
	}

	// Check file permissions.
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log perm = %o, want 0600", perm)
	}

	// Truncation of existing file.
	oldLog := filepath.Join(tmp, "old.log")
	if err := os.WriteFile(oldLog, []byte("OLD CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = RunArgv(ctx, []string{"printf", "new"}, ExecOptions{
		LogPath: oldLog,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(oldLog)
	if string(got) != "new" {
		t.Fatalf("old.log = %q, want %q", string(got), "new")
	}
}

// TS-04-13: An unopenable LogPath returns a zero result and an error, and the
// command never runs.
func TestTS_04_13_UnopenableLogPathReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix test")
	}

	ctx := context.Background()
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "marker")

	// blocker is a regular file, so blocker/run.log cannot be created.
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blockerLog := filepath.Join(blocker, "run.log")

	t.Run("RunArgv", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"touch", marker}, ExecOptions{
			LogPath: blockerLog,
			Timeout: 5 * time.Second,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if res != (ExecResult{}) {
			t.Fatalf("result = %+v, want zero", res)
		}
		if !strings.Contains(err.Error(), blockerLog) && !strings.Contains(err.Error(), blocker) {
			t.Fatalf("error = %q, want it to name the log path", err.Error())
		}
	})

	t.Run("Run", func(t *testing.T) {
		res, err := Run(ctx, "touch "+marker, ExecOptions{
			LogPath: blockerLog,
			Timeout: 5 * time.Second,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if res != (ExecResult{}) {
			t.Fatalf("result = %+v, want zero", res)
		}
	})

	// Read-only directory (skip if running as root).
	if os.Getuid() != 0 {
		t.Run("readonly_dir", func(t *testing.T) {
			roDir := filepath.Join(tmp, "ro")
			if err := os.MkdirAll(roDir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

			res, err := RunArgv(ctx, []string{"touch", marker}, ExecOptions{
				LogPath: filepath.Join(roDir, "run.log"),
				Timeout: 5 * time.Second,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if res != (ExecResult{}) {
				t.Fatalf("result = %+v, want zero", res)
			}
		})
	}

	// Marker must not exist after any of the calls.
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("marker file exists; the command ran despite the log error")
	}
}

// TS-04-14: LogPath replaces SpillDir: no temporary spill file, SpillPath is
// the log even for empty output, and the marker names it.
func TestTS_04_14_LogPathReplacesSpillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}

	ctx := context.Background()
	tmp := t.TempDir()
	spill := filepath.Join(tmp, "spill")
	if err := os.MkdirAll(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(tmp, "logs", "run.log")

	// Empty output.
	t.Run("empty_output", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"true"}, ExecOptions{
			SpillDir: spill,
			LogPath:  logFile,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		absLog, _ := filepath.Abs(logFile)
		if res.SpillPath != absLog {
			t.Fatalf("SpillPath = %q, want %q", res.SpillPath, absLog)
		}
		info, err := os.Stat(logFile)
		if err != nil {
			t.Fatalf("log file does not exist: %v", err)
		}
		if info.Size() != 0 {
			t.Fatalf("log size = %d, want 0", info.Size())
		}
	})

	// Truncated output.
	t.Run("truncated_output", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sh", "-c", "head -c 10240 /dev/zero | tr '\\0' x"}, ExecOptions{
			SpillDir: spill,
			LogPath:  logFile,
			MaxBytes: 100,
			Timeout:  5 * time.Second,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		absLog, _ := filepath.Abs(logFile)
		wantMarkerPart := "Full output: " + absLog + "]"
		if !strings.Contains(res.Output, wantMarkerPart) {
			t.Fatalf("Output = %q, want it to contain %q", res.Output, wantMarkerPart)
		}
	})

	// No agentkit-exec-*.log in spill dir.
	matches, _ := filepath.Glob(filepath.Join(spill, "agentkit-exec-*.log"))
	if len(matches) > 0 {
		t.Fatalf("spill dir contains temp files: %v", matches)
	}
}

// TS-04-16: The SDK never deletes a LogPath file, whatever the outcome.
func TestTS_04_16_LogPathNeverDeleted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and signals")
	}

	tmp := t.TempDir()

	cases := []struct {
		name        string
		argv        []string
		opts        ExecOptions
		cancelAfter time.Duration // 0 means no cancel
		wantOutcome Outcome
	}{
		{
			name:        "ok",
			argv:        []string{"sh", "-c", "echo ok"},
			wantOutcome: OutcomeOK,
		},
		{
			name:        "exit",
			argv:        []string{"sh", "-c", "exit 4"},
			wantOutcome: OutcomeExit,
		},
		{
			name:        "signal",
			argv:        []string{"sh", "-c", "kill -TERM $$"},
			wantOutcome: OutcomeSignal,
		},
		{
			name:        "timeout",
			argv:        []string{"sleep", "5"},
			opts:        ExecOptions{Timeout: 200 * time.Millisecond},
			wantOutcome: OutcomeTimeout,
		},
		{
			name:        "abort",
			argv:        []string{"sleep", "5"},
			cancelAfter: 200 * time.Millisecond,
			wantOutcome: OutcomeAbort,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logFile := filepath.Join(tmp, fmt.Sprintf("run_%d.log", i))
			opts := tc.opts
			opts.LogPath = logFile
			if opts.Timeout == 0 && tc.cancelAfter == 0 {
				opts.Timeout = 5 * time.Second
			}

			ctx := context.Background()
			if tc.cancelAfter > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.cancelAfter)
				defer cancel()
			}

			res, err := RunArgv(ctx, tc.argv, opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Outcome != tc.wantOutcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tc.wantOutcome)
			}
			if _, statErr := os.Stat(logFile); statErr != nil {
				t.Fatalf("log file does not exist after %s: %v", tc.wantOutcome, statErr)
			}
		})
	}

	// Verify the ok log contains "ok\n".
	got, _ := os.ReadFile(filepath.Join(tmp, "run_0.log"))
	if string(got) != "ok\n" {
		t.Fatalf("ok log = %q, want %q", string(got), "ok\n")
	}
}
