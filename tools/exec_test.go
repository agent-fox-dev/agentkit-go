package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestClassifyOutcomePrecedence(t *testing.T) {
	// Each row also sets every lower-precedence condition, so a wrong order
	// fails rather than agreeing by coincidence.
	for _, c := range []struct {
		aborted, timedOut bool
		code              int
		signaled          bool
		want              Outcome
	}{
		{true, true, 137, true, OutcomeAbort},
		{false, true, 137, true, OutcomeTimeout},
		{false, false, -1, true, OutcomeSignal},
		{false, false, 1, false, OutcomeExit},
		{false, false, 0, false, OutcomeOK},
	} {
		if got := ClassifyOutcome(c.aborted, c.timedOut, c.code, c.signaled); got != c.want {
			t.Errorf("ClassifyOutcome(%v, %v, %d, %v) = %q, want %q", c.aborted, c.timedOut, c.code, c.signaled, got, c.want)
		}
	}
}

// alive reports whether pid is a running process; a zombie is not.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err != nil || !strings.Contains(string(stat), ") Z ")
}

// TestEveryStartedCallEndsInExactlyOneOutcome runs each scenario through both
// entry points, with a log file, and checks the outcome, the exit code, that
// a killed command's whole process tree is gone and that the log survives.
func TestEveryStartedCallEndsInExactlyOneOutcome(t *testing.T) {
	needPOSIX(t)
	// A background grandchild records its pid and outlives its parent unless
	// the whole process tree is killed.
	const tree = "sleep 30 & echo $! > pid; sleep 30"
	for _, c := range []struct {
		name         string
		cmd          string
		timeout      time.Duration
		ctx          func() (context.Context, context.CancelFunc)
		outcome      Outcome
		code         int
		killsTheTree bool
	}{
		{name: "exit 0", cmd: "echo ok", outcome: OutcomeOK},
		{name: "exit 3", cmd: "exit 3", outcome: OutcomeExit, code: 3},
		{name: "SIGTERM is 128+15", cmd: "kill -TERM $$", outcome: OutcomeSignal, code: 143},
		{name: "SIGKILL is 128+9", cmd: "kill -KILL $$", outcome: OutcomeSignal, code: 137},
		{name: "timeout", cmd: tree, timeout: 200 * time.Millisecond, outcome: OutcomeTimeout, killsTheTree: true},
		{name: "cancel", cmd: tree, outcome: OutcomeAbort, killsTheTree: true, ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(200*time.Millisecond, cancel)
			return ctx, cancel
		}},
		{name: "a caller deadline shorter than the timeout is an abort", cmd: tree, timeout: 30 * time.Second,
			outcome: OutcomeAbort, killsTheTree: true, ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 200*time.Millisecond)
			}},
	} {
		for _, runner := range []string{"Run", "RunArgv"} {
			t.Run(c.name+"/"+runner, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				ctx, cancel := context.Background(), context.CancelFunc(func() {})
				if c.ctx != nil {
					ctx, cancel = c.ctx()
				}
				defer cancel()
				opts := ExecOptions{Dir: dir, Timeout: c.timeout, LogPath: "run.log", DrainIdle: 100 * time.Millisecond}
				var res ExecResult
				var err error
				if runner == "Run" {
					res, err = Run(ctx, c.cmd, opts)
				} else {
					res, err = RunArgv(ctx, []string{"sh", "-c", c.cmd}, opts)
				}
				if err != nil || res.IOErr != nil {
					t.Fatalf("err %v, IOErr %v; a started call returns neither", err, res.IOErr)
				}
				if res.Outcome != c.outcome || (!c.killsTheTree && res.ExitCode != c.code) {
					t.Fatalf("outcome %q exit %d, want %q exit %d", res.Outcome, res.ExitCode, c.outcome, c.code)
				}
				if res.SpillPath != filepath.Join(dir, "run.log") {
					t.Errorf("SpillPath = %q, want the log", res.SpillPath)
				}
				if _, err := os.Stat(res.SpillPath); err != nil {
					t.Errorf("the log is gone after a %s: %v", res.Outcome, err)
				}
				if !c.killsTheTree {
					return
				}
				if (c.outcome == OutcomeTimeout && res.Duration < c.timeout) || res.Duration > 5*time.Second {
					t.Errorf("Duration = %v, want at least the Timeout and well under the 30s sleep", res.Duration)
				}
				b, err := os.ReadFile(filepath.Join(dir, "pid"))
				pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
				if err != nil || perr != nil {
					t.Fatalf("the grandchild recorded no pid: %v %v", err, perr)
				}
				testutil.Eventually(t, "the grandchild to die with the tree", func() bool { return !alive(pid) })
			})
		}
	}
}

func TestOutputIsOneStreamInWriteOrder(t *testing.T) {
	needPOSIX(t)
	res, err := Run(context.Background(), `echo 1; echo 2 >&2; echo 3; echo 4 >&2`, ExecOptions{})
	if err != nil || res.Output != "1\n2\n3\n4\n" {
		t.Fatalf("output %q, %v: stdout and stderr must share one pipe", res.Output, err)
	}
}

func TestTheRetainedWindowFollowsMaxBytesAndKeepHead(t *testing.T) {
	needPOSIX(t)
	input := bytes.Repeat([]byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ"), DefaultByteLimit/26+1)[:DefaultByteLimit+1]
	for _, c := range []struct {
		name      string
		n, max    int
		head      bool
		truncated bool
	}{
		{"0 means the default, exactly filled", DefaultByteLimit, 0, false, false},
		{"a negative MaxBytes means the default", DefaultByteLimit, -1, true, false},
		{"one byte over the default", DefaultByteLimit + 1, 0, false, true},
		{"one byte over, keeping the head", DefaultByteLimit + 1, 0, true, true},
		{"an exact explicit limit", 64, 64, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := RunArgv(context.Background(), []string{"cat"}, ExecOptions{
				Stdin: bytes.NewReader(input[:c.n]), MaxBytes: c.max, KeepHead: c.head})
			if err != nil || res.Truncated != c.truncated || res.TotalBytes != int64(c.n) {
				t.Fatalf("truncated %v total %d err %v, want %v and %d", res.Truncated, res.TotalBytes, err, c.truncated, c.n)
			}
			want := string(input[:c.n])
			if c.truncated {
				marker := fmt.Sprintf("[1 bytes elided of %d total.]", c.n)
				want = marker + "\n" + want[1:]
				if c.head {
					want = string(input[:c.n-1]) + "\n" + marker
				}
			}
			if res.Output != want {
				t.Fatalf("output %.40q…, want %.40q…", res.Output, want)
			}
		})
	}
}

func TestStdin(t *testing.T) {
	needPOSIX(t)
	readErr := errors.New("source failed")
	big := make([]byte, 256<<10)
	for i := range big {
		big[i] = byte(i) // newlines and NULs included
	}
	for _, c := range []struct {
		name    string
		argv    []string
		stdin   io.Reader
		output  string
		outcome Outcome
		ioErr   error
	}{
		{"nil is the null device", []string{"sh", "-c", "cat; echo done"}, nil, "done\n", OutcomeOK, nil},
		{"a reader is copied byte for byte", []string{"cat"}, bytes.NewReader(big), string(big), OutcomeOK, nil},
		{"a child that reads nothing is not an error", []string{"true"}, io.LimitReader(zeros{}, 16<<20), "", OutcomeOK, nil},
		{"a child that reads a byte is not an error", []string{"head", "-c", "1"}, io.LimitReader(zeros{}, 16<<20), "\x00", OutcomeOK, nil},
		{"a child that closes stdin is not an error", []string{"sh", "-c", "exec 0<&-; echo closed"}, io.LimitReader(zeros{}, 16<<20), "closed\n", OutcomeOK, nil},
		{"a read error is reported, with the exit status", []string{"sh", "-c", "cat; exit 3"},
			io.MultiReader(strings.NewReader("partial\n"), errReader{readErr}), "partial\n", OutcomeExit, readErr},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := RunArgv(context.Background(), c.argv, ExecOptions{Stdin: c.stdin, MaxBytes: 1 << 20, Timeout: 10 * time.Second})
			if err != nil || res.Outcome != c.outcome || res.Output != c.output || !errors.Is(res.IOErr, c.ioErr) || (c.ioErr == nil) != (res.IOErr == nil) {
				t.Fatalf("outcome %q output %.30q IOErr %v err %v; want %q %.30q %v", res.Outcome, res.Output, res.IOErr, err, c.outcome, c.output, c.ioErr)
			}
		})
	}

	// A reader still blocked after the child exits does not hold the call,
	// and the copier ends once the reader returns.
	baseline := runtime.NumGoroutine()
	pr, pw := io.Pipe()
	res, err := RunArgv(context.Background(), []string{"true"}, ExecOptions{Stdin: pr, DrainIdle: 100 * time.Millisecond})
	if err != nil || res.Outcome != OutcomeOK || res.Duration > 2*time.Second {
		t.Fatalf("%+v %v", res, err)
	}
	_ = pw.CloseWithError(errors.New("late"))
	testutil.Eventually(t, "the stdin copier to end", func() bool { return runtime.NumGoroutine() <= baseline })
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestTheEnvironment(t *testing.T) {
	needPOSIX(t)
	t.Setenv("AGENTKIT_TEST_API_KEY", "sekrit")
	t.Setenv("MY_SERVICE_TOKEN", "tok")
	env := func(opts ExecOptions) string {
		t.Helper()
		res, err := RunArgv(context.Background(), []string{"env"}, opts)
		if err != nil {
			t.Fatal(err)
		}
		return res.Output
	}
	// Nil is ReducedEnv(nil), exactly, for both entry points.
	if got, want := env(ExecOptions{}), strings.Join(ReducedEnv(nil), "\n")+"\n"; got != want {
		t.Errorf("a nil Env is not the reduced environment:\n%s\nwant\n%s", got, want)
	}
	shell, err := Run(context.Background(), "env", ExecOptions{})
	if out := shell.Output; err != nil || strings.Contains(out, "sekrit") || strings.Contains(out, "MY_SERVICE_TOKEN") ||
		!strings.Contains(out, "PATH="+os.Getenv("PATH")+"\n") || !strings.Contains(out, "HOME="+os.Getenv("HOME")+"\n") {
		t.Errorf("Run with a nil Env is not the reduced environment:\n%s", out)
	}
	// A non-nil Env, empty included, is used verbatim.
	if got := env(ExecOptions{Env: []string{"FOO=1"}}); got != "FOO=1\n" {
		t.Errorf("Env {FOO=1}: %q", got)
	}
	if got := env(ExecOptions{Env: []string{}}); got != "" {
		t.Errorf("an empty Env: %q", got)
	}
	if got := env(ExecOptions{Env: os.Environ()}); !strings.Contains(got, "AGENTKIT_TEST_API_KEY=sekrit") {
		t.Errorf("os.Environ() restores the full environment: %q", got)
	}
}

func TestRunArgvLooksABareNameUpThroughTheChildsPATH(t *testing.T) {
	needPOSIX(t)
	binA, binB := t.TempDir(), t.TempDir()
	writeAt(t, binA, "agentkit-probe", "#!/bin/sh\necho A\n")
	writeAt(t, binB, "agentkit-probe", "#!/bin/sh\necho B\n")
	for _, d := range []string{binA, binB} {
		if err := os.Chmod(filepath.Join(d, "agentkit-probe"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", binA+sep+os.Getenv("PATH"))
	for _, c := range []struct {
		env  []string
		want string
	}{
		{nil, "A\n"}, // the reduced environment keeps the process PATH
		{[]string{"PATH=" + binB + sep + "/bin"}, "B\n"},
		{[]string{"PATH=relative" + sep + binB}, "B\n"}, // a relative entry is skipped
		{[]string{"FOO=1"}, "A\n"},                      // no PATH: the ordinary lookup
	} {
		res, err := RunArgv(context.Background(), []string{"agentkit-probe"}, ExecOptions{Env: c.env})
		if err != nil || res.Output != c.want {
			t.Errorf("Env %v: %q, %v; want %q", c.env, res.Output, err, c.want)
		}
	}
	if _, err := RunArgv(context.Background(), []string{"agentkit-probe"}, ExecOptions{Env: []string{"PATH=" + t.TempDir()}}); err == nil ||
		!strings.Contains(err.Error(), "agentkit-probe") {
		t.Errorf("a program absent from the child's PATH: %v", err)
	}
}

func TestACallThatCannotStartReturnsAZeroResultAndAnError(t *testing.T) {
	needPOSIX(t)
	tmp := t.TempDir()
	writeAt(t, tmp, "blocker", "x")
	marker := filepath.Join(tmp, "marker")
	ro := filepath.Join(tmp, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() (ExecResult, error){
		"an empty argv": func() (ExecResult, error) { return RunArgv(context.Background(), nil, ExecOptions{}) },
		"a missing program": func() (ExecResult, error) {
			return RunArgv(context.Background(), []string{"agentkit-no-such-program"}, ExecOptions{})
		},
		"a missing Dir": func() (ExecResult, error) {
			return RunArgv(context.Background(), []string{"touch", marker}, ExecOptions{Dir: filepath.Join(tmp, "missing")})
		},
		"a log under a file, RunArgv": func() (ExecResult, error) {
			return RunArgv(context.Background(), []string{"touch", marker}, ExecOptions{LogPath: filepath.Join(tmp, "blocker", "run.log")})
		},
		"a log under a file, Run": func() (ExecResult, error) {
			return Run(context.Background(), "touch "+marker, ExecOptions{LogPath: filepath.Join(tmp, "blocker", "run.log")})
		},
	}
	if os.Geteuid() != 0 {
		cases["a log in a read-only directory"] = func() (ExecResult, error) {
			return RunArgv(context.Background(), []string{"touch", marker}, ExecOptions{LogPath: filepath.Join(ro, "run.log")})
		}
	}
	for name, f := range cases {
		res, err := f()
		if err == nil || !reflect.DeepEqual(res, ExecResult{}) {
			t.Errorf("%s: %+v, %v; want a zero result and an error", name, res, err)
		}
	}
	if _, err := RunArgv(context.Background(), []string{"agentkit-no-such-program"}, ExecOptions{}); !strings.Contains(err.Error(), "agentkit-no-such-program") {
		t.Errorf("the error does not name the program: %v", err)
	}
	if _, err := Run(context.Background(), "true", ExecOptions{LogPath: filepath.Join(tmp, "blocker", "x")}); !strings.Contains(err.Error(), "blocker") {
		t.Errorf("the error does not name the log: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a command ran although its call could not start")
	}
}

func TestTheLogFile(t *testing.T) {
	needPOSIX(t)
	tmp := t.TempDir()
	var want strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&want, "out%d\nerr%d\n", i, i)
	}
	t.Run("holds the complete interleaved output in either mode", func(t *testing.T) {
		for _, head := range []bool{false, true} {
			log := filepath.Join(tmp, fmt.Sprintf("full-%v.log", head))
			res, err := RunArgv(context.Background(), []string{"sh", "-c", `i=1; while [ $i -le 2000 ]; do echo out$i; echo err$i >&2; i=$((i+1)); done`},
				ExecOptions{LogPath: log, MaxBytes: 1024, KeepHead: head})
			got, _ := os.ReadFile(log)
			if err != nil || string(got) != want.String() || int64(len(got)) != res.TotalBytes || !res.Truncated {
				t.Fatalf("head=%v: log %d bytes, total %d, err %v", head, len(got), res.TotalBytes, err)
			}
			if !strings.Contains(res.Output, "Full output: "+log+"]") {
				t.Fatalf("head=%v: the marker does not name the log: %q", head, res.Output[len(res.Output)-200:])
			}
		}
	})
	t.Run("a relative path is under Dir, or the working directory", func(t *testing.T) {
		res, err := RunArgv(context.Background(), []string{"true"}, ExecOptions{Dir: tmp, LogPath: "logs/run.log"})
		if err != nil || res.SpillPath != filepath.Join(tmp, "logs", "run.log") {
			t.Fatalf("SpillPath = %q, %v", res.SpillPath, err)
		}
		cwd := t.TempDir()
		t.Chdir(cwd)
		res, err = RunArgv(context.Background(), []string{"true"}, ExecOptions{LogPath: "run.log"})
		got, _ := filepath.EvalSymlinks(res.SpillPath)
		wantPath, _ := filepath.EvalSymlinks(filepath.Join(cwd, "run.log"))
		if err != nil || !filepath.IsAbs(res.SpillPath) || got != wantPath {
			t.Fatalf("SpillPath = %q, %v; want %s", res.SpillPath, err, wantPath)
		}
	})
	t.Run("exists before the child starts, private, truncated", func(t *testing.T) {
		log := filepath.Join(tmp, "a", "b", "run.log")
		res, err := RunArgv(context.Background(), []string{"sh", "-c", `test -f "$0" && echo present`, log}, ExecOptions{LogPath: log})
		if err != nil || res.Output != "present\n" {
			t.Fatalf("%q %v", res.Output, err)
		}
		for _, p := range []string{filepath.Join(tmp, "a"), filepath.Join(tmp, "a", "b")} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v %v, want 0700", p, fi.Mode(), err)
			}
		}
		if fi, err := os.Stat(log); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("the log: %v %v, want 0600", fi.Mode(), err)
		}
		if _, err := RunArgv(context.Background(), []string{"printf", "new"}, ExecOptions{LogPath: log}); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(log); string(got) != "new" {
			t.Errorf("an existing log is truncated: %q", got)
		}
	})
	t.Run("replaces the spill file", func(t *testing.T) {
		spill, log := filepath.Join(tmp, "spill"), filepath.Join(tmp, "replaces.log")
		res, err := RunArgv(context.Background(), []string{"true"}, ExecOptions{SpillDir: spill, LogPath: log})
		if fi, serr := os.Stat(log); err != nil || serr != nil || fi.Size() != 0 || res.SpillPath != log {
			t.Fatalf("an empty output still names the log: %q %v %v", res.SpillPath, err, serr)
		}
		res, _ = RunArgv(context.Background(), []string{"head", "-c", "10240", "/dev/zero"}, ExecOptions{SpillDir: spill, LogPath: log, MaxBytes: 100})
		if !strings.Contains(res.Output, "Full output: "+log+"]") {
			t.Fatal("the marker does not name the log")
		}
		if _, err := os.Stat(spill); err == nil {
			t.Fatal("a spill file was made although a log was named")
		}
	})
}

// TestTheOutputDrainAfterTheChildExits covers the descendant that keeps the
// pipe open: its output is drained while it keeps arriving, and a
// cancellation during the drain ends it without reclassifying the command.
func TestTheOutputDrainAfterTheChildExits(t *testing.T) {
	needPOSIX(t)
	t.Run("a writing descendant re-arms the idle timer", func(t *testing.T) {
		// The parent exits at once; the job writes every 100ms for 2s, far
		// past the 500ms idle window, so a drain that did not re-arm loses it.
		res, err := Run(context.Background(), `( i=0; while [ $i -lt 20 ]; do echo "late-$i"; sleep 0.1; i=$((i+1)); done ) &`,
			ExecOptions{DrainIdle: 500 * time.Millisecond})
		if err != nil || !strings.Contains(res.Output, "late-0") || !strings.Contains(res.Output, "late-19") {
			t.Fatalf("output %q, %v", res.Output, err)
		}
	})
	t.Run("the ceiling bounds a descendant that never stops", func(t *testing.T) {
		start := time.Now()
		res, err := Run(context.Background(), `( while :; do echo x; sleep 0.05; done ) & echo started`,
			ExecOptions{DrainIdle: time.Second, DrainCeiling: 300 * time.Millisecond, Timeout: 30 * time.Second})
		if err != nil || res.Outcome != OutcomeOK || time.Since(start) > 5*time.Second {
			t.Fatalf("%q %v after %v", res.Outcome, err, time.Since(start))
		}
	})
	for _, code := range []int{0, 3} {
		t.Run(fmt.Sprintf("a cancellation during the drain keeps exit %d", code), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(300*time.Millisecond, cancel)
			start := time.Now()
			res, err := RunArgv(ctx, []string{"sh", "-c", fmt.Sprintf("( sleep 30 ) & echo started; exit %d", code)},
				ExecOptions{DrainIdle: 30 * time.Second, DrainCeiling: 30 * time.Second})
			want := map[int]Outcome{0: OutcomeOK, 3: OutcomeExit}[code]
			if err != nil || res.Outcome != want || res.ExitCode != code || res.Output != "started\n" || time.Since(start) > 5*time.Second {
				t.Fatalf("%q exit %d %q %v after %v; want %q", res.Outcome, res.ExitCode, res.Output, err, time.Since(start), want)
			}
		})
	}
}

// failingFile is the spill or log writer: the first write succeeds, the rest fail.
type failingFile struct {
	err    error
	writes atomic.Int32
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.writes.Add(1) == 1 {
		return len(p), nil
	}
	return 0, f.err
}
func (f *failingFile) Close() error { return nil }

func TestAByteMovingFailureIsReportedInIOErrAndStopsNothing(t *testing.T) {
	needPOSIX(t)
	sentinel := errors.New("disk full")
	for _, dest := range []string{"log", "spill"} {
		t.Run(dest, func(t *testing.T) {
			ff := &failingFile{err: sentinel}
			orig := wrapExecFile
			wrapExecFile = func(*os.File) fileWriter { return ff }
			t.Cleanup(func() { wrapExecFile = orig })
			opts := ExecOptions{MaxBytes: 4096, LogPath: filepath.Join(t.TempDir(), "run.log")}
			if dest == "spill" {
				opts = ExecOptions{MaxBytes: 4096, SpillDir: t.TempDir()}
			}
			// 200 KB arrives in several reads, so there are writes after the failure.
			res, err := RunArgv(context.Background(), []string{"sh", "-c", "head -c 200000 /dev/zero; exit 3"}, opts)
			if err != nil || !errors.Is(res.IOErr, sentinel) || res.Outcome != OutcomeExit || res.ExitCode != 3 || res.TotalBytes != 200000 {
				t.Fatalf("%q exit %d total %d IOErr %v err %v", res.Outcome, res.ExitCode, res.TotalBytes, res.IOErr, err)
			}
			if n := ff.writes.Load(); n != 2 {
				t.Fatalf("%d writes reached the file; it must stop at the first failure", n)
			}
		})
	}
	if runtime.GOOS == "linux" {
		res, err := RunArgv(context.Background(), []string{"sh", "-c", "echo x; exit 3"}, ExecOptions{LogPath: "/dev/full"})
		if err != nil || res.IOErr == nil || res.Outcome != OutcomeExit {
			t.Fatalf("/dev/full: %+v %v", res, err)
		}
	}
}

func TestAWaitErrorLandsInIOErrAndTheStatusStillCounts(t *testing.T) {
	needPOSIX(t)
	orig := waitCmd
	waitCmd = func(c *exec.Cmd) error { _ = c.Wait(); return exec.ErrWaitDelay }
	t.Cleanup(func() { waitCmd = orig })
	for _, c := range []struct {
		cmd     string
		outcome Outcome
		code    int
	}{{"exit 0", OutcomeOK, 0}, {"exit 4", OutcomeExit, 4}, {"kill -KILL $$", OutcomeSignal, 137}} {
		res, err := RunArgv(context.Background(), []string{"sh", "-c", c.cmd}, ExecOptions{})
		if err != nil || !errors.Is(res.IOErr, exec.ErrWaitDelay) || res.Outcome != c.outcome || res.ExitCode != c.code {
			t.Errorf("%s: %q exit %d IOErr %v err %v", c.cmd, res.Outcome, res.ExitCode, res.IOErr, err)
		}
	}
}

func TestATimedOutCallsDurationCoversItsSetup(t *testing.T) {
	needPOSIX(t)
	// Opening a named pipe for writing waits for a reader, so the call sits in
	// its setup until the reader below opens the log; that time is inside the
	// Timeout and so must be inside the Duration.
	log := filepath.Join(t.TempDir(), "log")
	if err := syscall.Mkfifo(log, 0o600); err != nil {
		t.Skipf("no named pipes here: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(100 * time.Millisecond) // the slow setup being measured
		if f, err := os.Open(log); err == nil {
			_, _ = io.Copy(io.Discard, f)
			f.Close()
		}
	}()
	res, err := RunArgv(context.Background(), []string{"sleep", "30"}, ExecOptions{LogPath: log, Timeout: 200 * time.Millisecond})
	<-done
	if err != nil || res.Outcome != OutcomeTimeout || res.Duration < 200*time.Millisecond {
		t.Fatalf("%q after %v, %v; want a timeout of at least its 200ms Timeout", res.Outcome, res.Duration, err)
	}
}

func TestReducedEnvStripsCredentialsAndKeepsWhatACommandNeeds(t *testing.T) {
	env := ReducedEnv([]string{
		"PATH=/usr/bin", "HOME=/h", "LANG=C.UTF-8", "TMPDIR=/tmp", "TERM=xterm", "EDITOR=vim",
		"AWS_PROFILE=dev", "AWS_REGION=eu-west-1", "AWS_DEFAULT_REGION=us-east-1", "GOOGLE_CLOUD_PROJECT=proj",
		"ANTHROPIC_API_KEY=x1", "anthropic_api_key=x2", "OPENAI_API_KEY=x3", "aws_secret_access_key=x4", "AWS_ACCESS_KEY_ID=x5",
		"GITHUB_TOKEN=x6", "NPM_TOKEN=x7", "DB_PASSWORD=x8", "STRIPE_SECRET=x9", "SOME_API_KEY=x10", "vault_token=x11",
		"GOOGLE_APPLICATION_CREDENTIALS=x12", "GITHUB_PAT=x13", "DOCKER_AUTH=x14", "DB_PASS=x15", "DB_PWD=x16",
		"SSH_PRIVATE_KEY=x17", "SIGNING_KEY=x18", "TOKEN=x19", "API_KEY=x20", "SECRET=x21", "PASSWORD=x22",
		"MYCO_URL=x23", "malformed",
	}, "myco_")
	want := []string{"PATH=/usr/bin", "HOME=/h", "LANG=C.UTF-8", "TMPDIR=/tmp", "TERM=xterm", "EDITOR=vim",
		"AWS_PROFILE=dev", "AWS_REGION=eu-west-1", "AWS_DEFAULT_REGION=us-east-1", "GOOGLE_CLOUD_PROJECT=proj"}
	if !reflect.DeepEqual(env, want) {
		t.Fatalf("ReducedEnv kept %v\nwant %v", env, want)
	}
	shell, args, err := ResolveShell()
	if err != nil || !strings.HasSuffix(shell, "bash") && !strings.HasSuffix(shell, "sh") || !reflect.DeepEqual(args, []string{"-c"}) {
		t.Fatalf("ResolveShell = %q %v %v", shell, args, err)
	}
}
