package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

type outcomeScenario struct {
	name         string
	cmd          string
	timeout      time.Duration
	ctxTimeout   time.Duration // 0 means no caller deadline
	cancelAfter  time.Duration // 0 means no cancel
	wantOutcome  Outcome
	wantExitCode int
}

// generateOutcomeScenarios builds at least 30 deterministic scenarios.
func generateOutcomeScenarios() []outcomeScenario {
	rng := rand.New(rand.NewSource(42))
	var scenarios []outcomeScenario

	for i := 0; i < 15; i++ {
		k := rng.Intn(126)
		want := OutcomeExit
		if k == 0 {
			want = OutcomeOK
		}
		scenarios = append(scenarios, outcomeScenario{
			name: fmt.Sprintf("exit_%d", k), cmd: fmt.Sprintf("exit %d", k),
			wantOutcome: want, wantExitCode: k,
		})
	}

	scenarios = append(scenarios,
		outcomeScenario{name: "self_sigterm", cmd: "kill -TERM $$", wantOutcome: OutcomeSignal, wantExitCode: 143},
		outcomeScenario{name: "self_sigkill", cmd: "kill -KILL $$", wantOutcome: OutcomeSignal, wantExitCode: 137},
	)

	for i := 0; i < 5; i++ {
		ms := 100 + rng.Intn(100)
		scenarios = append(scenarios, outcomeScenario{
			name: fmt.Sprintf("timeout_%dms", ms), cmd: "sleep 10",
			timeout: time.Duration(ms) * time.Millisecond, wantOutcome: OutcomeTimeout,
		})
	}

	for i := 0; i < 5; i++ {
		ms := 100 + rng.Intn(100)
		scenarios = append(scenarios, outcomeScenario{
			name: fmt.Sprintf("cancel_%dms", ms), cmd: "sleep 10",
			cancelAfter: time.Duration(ms) * time.Millisecond, wantOutcome: OutcomeAbort,
		})
	}

	for i := 0; i < 3; i++ {
		ms := 100 + rng.Intn(100)
		scenarios = append(scenarios, outcomeScenario{
			name: fmt.Sprintf("caller_deadline_%dms", ms), cmd: "sleep 10",
			timeout: 10 * time.Second, ctxTimeout: time.Duration(ms) * time.Millisecond,
			wantOutcome: OutcomeAbort,
		})
	}

	for i := 0; i < 5; i++ {
		k := rng.Intn(5)
		want := OutcomeExit
		if k == 0 {
			want = OutcomeOK
		}
		scenarios = append(scenarios, outcomeScenario{
			name:        fmt.Sprintf("exit_%d_cancel_during_drain", k),
			cmd:         fmt.Sprintf("( sleep 3 ) & echo started; exit %d", k),
			cancelAfter: 300 * time.Millisecond, wantOutcome: want, wantExitCode: k,
		})
	}

	return scenarios
}

// TS-04-20: Every started call returns a nil error and exactly the one
// Outcome its scenario implies (property test).
func TestTS_04_20_OutcomePropertyTest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix signals")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	scenarios := generateOutcomeScenarios()
	if len(scenarios) < 30 {
		t.Fatalf("only %d scenarios, want at least 30", len(scenarios))
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			for _, runner := range []string{"RunArgv", "Run"} {
				runner := runner
				t.Run(runner, func(t *testing.T) {
					ctx := context.Background()
					var cancel context.CancelFunc

					if sc.ctxTimeout > 0 {
						ctx, cancel = context.WithTimeout(ctx, sc.ctxTimeout)
						defer cancel()
					} else if sc.cancelAfter > 0 {
						ctx, cancel = context.WithCancel(ctx)
						time.AfterFunc(sc.cancelAfter, cancel)
						defer cancel()
					}

					opts := ExecOptions{
						Timeout:      sc.timeout,
						DrainIdle:    100 * time.Millisecond,
						DrainCeiling: 2 * time.Second,
					}

					var res ExecResult
					var err error
					if runner == "RunArgv" {
						res, err = RunArgv(ctx, []string{"sh", "-c", sc.cmd}, opts)
					} else {
						res, err = Run(ctx, sc.cmd, opts)
					}

					if err != nil {
						t.Fatalf("error = %v, want nil", err)
					}
					if res.Outcome != sc.wantOutcome {
						t.Fatalf("Outcome = %q, want %q", res.Outcome, sc.wantOutcome)
					}
					if sc.wantOutcome == OutcomeOK || sc.wantOutcome == OutcomeExit || sc.wantOutcome == OutcomeSignal {
						if res.ExitCode != sc.wantExitCode {
							t.Fatalf("ExitCode = %d, want %d", res.ExitCode, sc.wantExitCode)
						}
					}
				})
			}
		})
	}
}

// TS-04-21: Cancelling the caller's ctx kills the whole process tree through
// RunArgv and classifies the call as abort.
func TestTS_04_21_CancelKillsTreeAndClassifiesAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix process groups")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	defer cancel()

	cmd := "( sleep 1; touch " + marker + " ) & sleep 5"
	res, err := RunArgv(ctx, []string{"sh", "-c", cmd}, ExecOptions{Dir: dir})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if res.Outcome != OutcomeAbort {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeAbort)
	}
	if res.Duration < 150*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 150ms", res.Duration)
	}
	if res.Duration >= 1*time.Second {
		t.Fatalf("Duration = %v, want < 1s", res.Duration)
	}

	// Wait past when the grandchild would have written.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the grandchild survived the kill and wrote its marker")
	}
}

// TS-04-22: Timeout expiry kills the whole process tree through RunArgv and
// classifies the call as timeout.
func TestTS_04_22_TimeoutKillsTreeAndClassifiesTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix process groups")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")

	ctx := context.Background()
	cmd := "( sleep 1; touch " + marker + " ) & sleep 5"
	res, err := RunArgv(ctx, []string{"sh", "-c", cmd}, ExecOptions{
		Dir:     dir,
		Timeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if res.Outcome != OutcomeTimeout {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeTimeout)
	}
	if res.Duration < 200*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 200ms", res.Duration)
	}
	if res.Duration >= 1*time.Second {
		t.Fatalf("Duration = %v, want < 1s", res.Duration)
	}

	// Wait past when the grandchild would have written.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the grandchild survived the timeout and wrote its marker")
	}
}

// TS-04-23: A caller deadline shorter than Timeout is classified as abort,
// not timeout.
func TestTS_04_23_CallerDeadlineShorterThanTimeoutIsAbort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix process groups")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "grandchild-survived")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	cmd := "( sleep 1; touch " + marker + " ) & sleep 5"
	res, err := RunArgv(ctx, []string{"sh", "-c", cmd}, ExecOptions{
		Dir:     dir,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if res.Outcome != OutcomeAbort {
		t.Fatalf("Outcome = %q, want %q (not timeout)", res.Outcome, OutcomeAbort)
	}
	if res.Duration >= 1*time.Second {
		t.Fatalf("Duration = %v, want < 1s", res.Duration)
	}

	// Wait past when the grandchild would have written.
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the grandchild survived and wrote its marker")
	}
}

// TS-04-24: A cancellation during the post-exit drain does not reclassify a
// command that already finished.
func TestTS_04_24_CancelDuringDrainDoesNotReclassify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	for _, tc := range []struct {
		exitCode    int
		wantOutcome Outcome
	}{
		{0, OutcomeOK},
		{3, OutcomeExit},
	} {
		tc := tc
		t.Run(fmt.Sprintf("exit_%d", tc.exitCode), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(300*time.Millisecond, cancel)
			defer cancel()

			cmd := fmt.Sprintf("( sleep 3 ) & echo started; exit %d", tc.exitCode)
			start := time.Now()
			res, err := RunArgv(ctx, []string{"sh", "-c", cmd}, ExecOptions{
				DrainIdle:    10 * time.Second,
				DrainCeiling: 10 * time.Second,
			})
			elapsed := time.Since(start)

			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if res.Outcome == OutcomeAbort {
				t.Fatalf("Outcome = abort, want %q (cancel arrived during drain, not during run)", tc.wantOutcome)
			}
			if res.Outcome != tc.wantOutcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tc.wantOutcome)
			}
			if res.ExitCode != tc.exitCode {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, tc.exitCode)
			}
			if !strings.Contains(res.Output, "started") {
				t.Fatalf("Output = %q, want to contain %q", res.Output, "started")
			}
			if elapsed >= 2*time.Second {
				t.Fatalf("call took %v, want < 2s (drain should stop on cancellation)", elapsed)
			}
		})
	}
}

// TS-04-25: A child that exits on its own is classified ok, exit or signal
// from its wait status, with 128+signum for a signal on unix.
func TestTS_04_25_ExitClassification(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix signals")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	ctx := context.Background()

	cases := []struct {
		cmd      string
		outcome  Outcome
		exitCode int
	}{
		{"exit 0", OutcomeOK, 0},
		{"exit 3", OutcomeExit, 3},
		{"kill -TERM $$", OutcomeSignal, 143},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.cmd, func(t *testing.T) {
			res, err := RunArgv(ctx, []string{"sh", "-c", tc.cmd}, ExecOptions{Timeout: 5 * time.Second})
			if err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
			if res.IOErr != nil {
				t.Fatalf("IOErr = %v, want nil", res.IOErr)
			}
			if res.Outcome != tc.outcome {
				t.Fatalf("Outcome = %q, want %q", res.Outcome, tc.outcome)
			}
			if res.ExitCode != tc.exitCode {
				t.Fatalf("ExitCode = %d, want %d", res.ExitCode, tc.exitCode)
			}
		})
	}
}

// TS-04-26: Exactly the start failures return a zero ExecResult and a non-nil
// error.
func TestTS_04_26_StartFailuresReturnZeroResultAndError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses sh -c and unix paths")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	ctx := context.Background()
	tmp := t.TempDir()

	// Create a regular file to block LogPath's parent creation.
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	zero := ExecResult{}

	// Start failures: each returns ExecResult{} and a non-nil error.
	t.Run("empty_argv", func(t *testing.T) {
		res, err := RunArgv(ctx, nil, ExecOptions{})
		if err == nil {
			t.Fatal("expected error for empty argv")
		}
		if !reflect.DeepEqual(res, zero) {
			t.Fatalf("result = %+v, want zero ExecResult", res)
		}
	})

	t.Run("program_not_found", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"agentkit-no-such-program-04"}, ExecOptions{})
		if err == nil {
			t.Fatal("expected error for missing program")
		}
		if !strings.Contains(err.Error(), "agentkit-no-such-program-04") {
			t.Fatalf("error = %v, want to mention the program name", err)
		}
		if !reflect.DeepEqual(res, zero) {
			t.Fatalf("result = %+v, want zero ExecResult", res)
		}
	})

	t.Run("missing_dir", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"true"}, ExecOptions{Dir: filepath.Join(tmp, "missing")})
		if err == nil {
			t.Fatal("expected error for missing Dir")
		}
		if !reflect.DeepEqual(res, zero) {
			t.Fatalf("result = %+v, want zero ExecResult", res)
		}
	})

	t.Run("logpath_unopenable", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"true"}, ExecOptions{
			LogPath: filepath.Join(blocker, "run.log"),
		})
		if err == nil {
			t.Fatal("expected error for unopenable LogPath")
		}
		if !reflect.DeepEqual(res, zero) {
			t.Fatalf("result = %+v, want zero ExecResult", res)
		}
	})

	// Started calls: each returns nil error with the expected Outcome.
	t.Run("started_exit3", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sh", "-c", "exit 3"}, ExecOptions{Timeout: 5 * time.Second})
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if res.Outcome != OutcomeExit {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeExit)
		}
	})

	t.Run("started_timeout", func(t *testing.T) {
		res, err := RunArgv(ctx, []string{"sleep", "5"}, ExecOptions{Timeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if res.Outcome != OutcomeTimeout {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeTimeout)
		}
	})

	t.Run("started_abort", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		defer cancel()

		res, err := RunArgv(cancelCtx, []string{"sleep", "5"}, ExecOptions{})
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if res.Outcome != OutcomeAbort {
			t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeAbort)
		}
	})
}

// TS-06-18: execute and run_command share one output schema, and
// a non-zero exit still fills Data with output, exit_code and outcome that
// conform to it.
func TestOutputSchemaSubprocessTools_TS06_18(t *testing.T) {
	dir := t.TempDir()
	ex := toolByName(t, dir, "execute")
	rc := toolByName(t, dir, "run_command")
	if ex.OutputSchema == nil || ex.OutputSchema != rc.OutputSchema {
		t.Fatalf("schemas %p %p, want one shared non-nil schema", ex.OutputSchema, rc.OutputSchema)
	}
	assertObject(t, "subprocess", ex.OutputSchema, map[string]prop{
		"output":    {schema.TypeString, true},
		"exit_code": {schema.TypeInteger, true},
		"outcome":   {schema.TypeString, true},
	})
	var enum []string
	for _, raw := range ex.OutputSchema.Properties["outcome"].Enum {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		enum = append(enum, v)
	}
	if want := []string{"ok", "exit", "signal", "timeout", "abort"}; !reflect.DeepEqual(enum, want) {
		t.Fatalf("outcome enum = %v, want %v", enum, want)
	}
	if runtime.GOOS == "windows" {
		t.Skip("exit 3 is a POSIX shell command")
	}

	for name, tc := range map[string]struct {
		tool core.Tool
		args string
	}{
		"execute":     {ex, `{"command":"echo out; exit 3"}`},
		"run_command": {rc, `{"argv":["sh","-c","echo out; exit 3"]}`},
	} {
		res := tc.tool.Execute(context.Background(), json.RawMessage(tc.args))
		if res.OK || res.Error != "command_exit" {
			t.Fatalf("%s: OK %v error %q, want command_exit", name, res.OK, res.Error)
		}
		if res.Data["exit_code"] != 3 || res.Data["outcome"] != "exit" || !strings.Contains(fmt.Sprint(res.Data["output"]), "out") {
			t.Fatalf("%s: Data = %+v, want output, exit_code 3, outcome exit", name, res.Data)
		}
		blob, err := json.Marshal(res.Data)
		if err != nil {
			t.Fatal(err)
		}
		ord, err := jsonObject(blob)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(tc.tool.OutputSchema, ord); err != nil {
			t.Fatalf("%s: Data does not conform: %v", name, err)
		}
	}
}

// jsonObject decodes a JSON object as schema.Validate takes it: numbers as
// json.Number.
func jsonObject(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	err := d.Decode(&m)
	return m, err
}
