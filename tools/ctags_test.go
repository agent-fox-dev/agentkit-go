package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TS-01-43: CtagsRunner's return value is assignable to outline.Options.Runner
// and ctags.go does not import outline.
//
// The assignability check is in tools/ctags_assign_test.go (external test
// package that can import both outline and tools). This test verifies that
// ctags.go itself does not import outline — the tools package as a whole may
// import it for the file_outline and find_symbol tools added by spec 02.
func TestCtagsRunner_NoOutlineImport_TS_01_43(t *testing.T) {
	// Read ctags.go and verify it does not contain an outline import.
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), "tools", "ctags.go"))
	if err != nil {
		t.Fatalf("reading ctags.go: %v", err)
	}
	if strings.Contains(string(data), `"github.com/agentfox/agentkit-go/outline"`) {
		t.Fatal("ctags.go imports outline; it must not")
	}
}

// TS-01-44: With ctags absent from PATH every call returns an error wrapping
// ErrCtagsUnavailable.
func TestCtagsRunner_NoCtagsOnPath_TS_01_44(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: empty PATH handling differs")
	}
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	r := CtagsRunner(nil)

	ctx := context.Background()
	args := []string{"--output-format=json", "-f", "-", "/dev/null"}

	out1, err1 := r(ctx, args)
	if !errors.Is(err1, ErrCtagsUnavailable) {
		t.Fatalf("first call: want ErrCtagsUnavailable, got %v", err1)
	}
	if out1 != nil {
		t.Fatalf("first call: want nil stdout, got %d bytes", len(out1))
	}

	out2, err2 := r(ctx, args)
	if !errors.Is(err2, ErrCtagsUnavailable) {
		t.Fatalf("second call: want ErrCtagsUnavailable, got %v", err2)
	}
	if out2 != nil {
		t.Fatalf("second call: want nil stdout, got %d bytes", len(out2))
	}
}

// TS-01-45: LookPath and the version check run once per runner and only
// Universal Ctags is accepted.
func TestCtagsRunner_VersionCheck_TS_01_45(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell scripts not supported")
	}

	type variant struct {
		name      string
		output    string
		wantAvail bool
	}
	variants := []variant{
		{"exuberant", "Exuberant Ctags 5.8, Copyright (C) 1996-2009\n", false},
		{"bsd", "usage: ctags [-BFadtuwvx] [-f tagsfile] file ...\n", false},
		{"universal", "Universal Ctags 6.0.0, Copyright (C) 2015-2023\n", true},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			binDir := t.TempDir()
			counterFile := filepath.Join(t.TempDir(), "count")
			if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
				t.Fatal(err)
			}

			// Create a fake ctags script that counts --version calls.
			script := filepath.Join(binDir, "ctags")
			content := "#!/bin/sh\n" +
				"if [ \"$1\" = \"--version\" ]; then\n" +
				"  count=$(cat " + counterFile + ")\n" +
				"  count=$((count + 1))\n" +
				"  printf '%d' \"$count\" > " + counterFile + "\n" +
				"  printf '%s' '" + v.output + "'\n" +
				"  exit 0\n" +
				"fi\n"
			if v.wantAvail {
				// For universal variant, echo args to stdout when not --version.
				content += "echo \"ARGS: $@\"\nexit 0\n"
			} else {
				content += "exit 1\n"
			}
			if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
				t.Fatal(err)
			}

			t.Setenv("PATH", binDir)

			r := CtagsRunner(nil)
			ctx := context.Background()
			args := []string{"--output-format=json", "-f", "-"}

			for i := 0; i < 3; i++ {
				out, err := r(ctx, args)
				if v.wantAvail {
					if err != nil {
						t.Fatalf("call %d: unexpected error: %v", i, err)
					}
					if out == nil {
						t.Fatalf("call %d: want non-nil stdout", i)
					}
				} else {
					if !errors.Is(err, ErrCtagsUnavailable) {
						t.Fatalf("call %d: want ErrCtagsUnavailable, got %v", i, err)
					}
					if out != nil {
						t.Fatalf("call %d: want nil stdout, got %d bytes", i, len(out))
					}
				}
			}

			// Check that --version was called exactly once.
			countData, err := os.ReadFile(counterFile)
			if err != nil {
				t.Fatal(err)
			}
			if string(countData) != "1" {
				t.Fatalf("--version called %s times, want 1", string(countData))
			}
		})
	}
}

// TS-01-46: ctags runs with the given or reduced environment, empty stdin and
// discarded stderr, and stdout is captured alone.
func TestCtagsRunner_Environment_TS_01_46(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell scripts not supported")
	}

	binDir := t.TempDir()
	// Create a fake universal ctags script that:
	// - On --version: prints Universal Ctags
	// - Otherwise: prints env to stdout, reads stdin, writes marker to stderr
	script := filepath.Join(binDir, "ctags")
	content := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "Universal Ctags 6.0.0"
  exit 0
fi
# Print environment to stdout
env
# Try to read stdin (should be empty/EOF)
if read -t 1 line 2>/dev/null; then
  echo "STDIN_DATA: $line"
else
  echo "STDIN_EMPTY"
fi
# Write marker to stderr
echo "STDERR_MARKER_12345" >&2
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)
	// Set a secret variable that ReducedEnv should strip.
	t.Setenv("MY_SECRET_TOKEN", "supersecret")

	ctx := context.Background()
	args := []string{"--output-format=json", "-f", "-"}

	t.Run("nil_env_uses_ReducedEnv", func(t *testing.T) {
		r := CtagsRunner(nil)
		out, err := r(ctx, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		s := string(out)
		// The secret token should be stripped by ReducedEnv.
		if strings.Contains(s, "MY_SECRET_TOKEN") {
			t.Error("stdout contains MY_SECRET_TOKEN; ReducedEnv should have stripped it")
		}
		if strings.Contains(s, "supersecret") {
			t.Error("stdout contains the secret value")
		}
		// stderr marker should NOT appear in stdout.
		if strings.Contains(s, "STDERR_MARKER_12345") {
			t.Error("stderr marker appeared in stdout; stderr should be discarded")
		}
		// stdin should be empty.
		if !strings.Contains(s, "STDIN_EMPTY") {
			t.Error("stdin was not empty")
		}
	})

	t.Run("explicit_env", func(t *testing.T) {
		// Include a minimal PATH so the shell script can find basic commands.
		r := CtagsRunner([]string{"FOO=bar", "PATH=" + binDir + ":/usr/bin:/bin"})
		out, err := r(ctx, args)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		s := string(out)
		if !strings.Contains(s, "FOO=bar") {
			t.Error("stdout does not contain FOO=bar")
		}
		// The secret should not be present with explicit env.
		if strings.Contains(s, "supersecret") {
			t.Error("stdout contains the secret value with explicit env")
		}
	})
}

// TS-01-47: Truncated output returns an error rather than a cut-off stream.
func TestCtagsRunner_Truncation_TS_01_47(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell scripts not supported")
	}

	binDir := t.TempDir()
	// Create a fake universal ctags that emits more than 1 KiB on stdout.
	script := filepath.Join(binDir, "ctags")
	content := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "Universal Ctags 6.0.0"
  exit 0
fi
# Emit 2 KiB of output using printf in a loop
i=0
while [ $i -lt 64 ]; do
  printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
  i=$((i + 1))
done
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)

	// Lower the accumulator cap to 1 KiB.
	old := ctagsAccumulatorCap
	ctagsAccumulatorCap = 1024
	defer func() { ctagsAccumulatorCap = old }()

	r := CtagsRunner(nil)
	ctx := context.Background()
	args := []string{"--output-format=json", "-f", "-"}

	out, err := r(ctx, args)
	if err == nil {
		t.Fatalf("expected error for truncated output, got %d bytes of output", len(out))
	}
	if out != nil {
		t.Fatalf("want nil stdout on truncation, got %d bytes", len(out))
	}
}

// TS-01-48: Context cancellation kills the whole ctags process group.
func TestCtagsRunner_ContextCancellation_TS_01_48(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell scripts and process groups differ")
	}

	binDir := t.TempDir()
	// Save the original PATH so the shell script can find sleep.
	origPath := os.Getenv("PATH")

	// Create a fake universal ctags that sleeps for a long time.
	// Use the full path to sleep to avoid PATH issues.
	script := filepath.Join(binDir, "ctags")
	content := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then\n" +
		"  echo \"Universal Ctags 6.0.0\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"# Sleep for a long time (will be killed by context cancellation).\n" +
		"export PATH=" + origPath + "\n" +
		"sleep 60\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	// Set PATH to include binDir and system dirs so the runner can find
	// the fake ctags and the shell can find sleep.
	t.Setenv("PATH", binDir+":"+origPath)

	r := CtagsRunner(nil)
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"--output-format=json", "-f", "-"}

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		_, runErr = r(ctx, args)
	}()

	// Give the process a moment to start, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// The call should return promptly.
	select {
	case <-done:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("CtagsRunner did not return within 5 seconds after cancellation")
	}

	if runErr == nil {
		t.Fatal("expected error after context cancellation")
	}
}

// TS-01-49: A non-zero ctags exit status returns an error and no stdout.
func TestCtagsRunner_NonZeroExit_TS_01_49(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: shell scripts not supported")
	}

	binDir := t.TempDir()
	// Create a fake universal ctags that exits with status 3.
	script := filepath.Join(binDir, "ctags")
	content := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "Universal Ctags 6.0.0"
  exit 0
fi
echo "some output before failure"
exit 3
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", binDir)

	r := CtagsRunner(nil)
	ctx := context.Background()
	args := []string{"--output-format=json", "-f", "-"}

	out, err := r(ctx, args)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if errors.Is(err, ErrCtagsUnavailable) {
		t.Fatal("error should not be ErrCtagsUnavailable for a non-zero exit")
	}
	if out != nil {
		t.Fatalf("want nil stdout on non-zero exit, got %d bytes", len(out))
	}
}

// moduleRoot returns the module root directory for test setup.
func moduleRoot(t *testing.T) string {
	t.Helper()
	// We're in tools/, so the module root is one level up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

// TestCtagsInstalledLaterIsPickedUp (issue #89): "unavailable" is a verdict
// about this moment, not forever. A runner that probed before ctags was
// installed probes again once ctagsRetryInterval has passed, through the
// PATH its own environment carries.
func TestCtagsInstalledLaterIsPickedUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ctags is a shell script")
	}
	dir := t.TempDir()
	prev := ctagsRetryInterval
	ctagsRetryInterval = 0
	defer func() { ctagsRetryInterval = prev }()

	run := CtagsRunner([]string{"PATH=" + dir + ":/bin:/usr/bin"})
	if _, err := run(context.Background(), []string{"--version"}); !errors.Is(err, ErrCtagsUnavailable) {
		t.Fatalf("err = %v before install, want ErrCtagsUnavailable", err)
	}
	fake := "#!/bin/sh\necho 'Universal Ctags 6.2.1'\n"
	if err := os.WriteFile(filepath.Join(dir, "ctags"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := run(context.Background(), []string{"--version"})
	if err != nil {
		t.Fatalf("ctags installed after the first probe was never picked up: %v", err)
	}
	if !strings.Contains(string(out), "Universal Ctags") {
		t.Fatalf("output = %q, want the fake ctags's", out)
	}
}
