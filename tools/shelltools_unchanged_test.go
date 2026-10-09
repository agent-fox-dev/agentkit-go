package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// ------------------------------------------------------------------ TS-04-30
// Frozen copies of execResultToTool and execStatusLine as they were before
// this spec. Any change to the live functions must produce identical output
// for every ExecResult that does not use the new IOErr field.

func preChangeExecStatusLine(res ExecResult, timeout time.Duration) string {
	switch res.Outcome {
	case OutcomeExit:
		return fmt.Sprintf("[exit %d]", res.ExitCode)
	case OutcomeTimeout:
		if timeout > 0 {
			return fmt.Sprintf("[timeout after %s]", timeout)
		}
		return "[timeout]"
	case OutcomeAbort:
		return "[aborted]"
	case OutcomeSignal:
		if res.ExitCode > 128 {
			return fmt.Sprintf("[killed by signal %d]", res.ExitCode-128)
		}
		return "[killed by signal]"
	}
	return ""
}

func preChangeExecResultToTool(res ExecResult, timeout time.Duration) core.ToolResult {
	code := res.ExitCode
	md := &core.ToolMetadata{
		Truncated:  res.Truncated,
		TotalBytes: res.TotalBytes,
		SpillPath:  res.SpillPath,
		DurationMS: res.Duration.Milliseconds(),
		ExitCode:   &code,
		Outcome:    string(res.Outcome),
	}
	if res.Truncated {
		md.TruncatedBy = string(TruncatedByBytes)
	}
	out := core.ToolResult{
		OK:       res.Outcome == OutcomeOK,
		Data:     map[string]any{"output": res.Output, "exit_code": code, "outcome": string(res.Outcome)},
		Metadata: md,
	}
	if !out.OK {
		out.Error = "command_" + string(res.Outcome)
	}
	out.Text = res.Output
	if status := preChangeExecStatusLine(res, timeout); status != "" {
		if out.Text != "" && !strings.HasSuffix(out.Text, "\n") {
			out.Text += "\n"
		}
		out.Text += status
	}
	return out
}

// ------------------------------------------------------------------ TS-04-28
// The shell tools run with their explicit Options.Env, tail truncation at
// DefaultByteLimit, Options.SpillDir and no log.

// ts0428Setup creates the workspace, tools and payload for TS-04-28 tests.
func ts0428Setup(t *testing.T) ([]core.Tool, func(string) core.Tool, string, int, string) {
	t.Helper()
	root := t.TempDir()
	spillDir := filepath.Join(root, "spill")

	t.Setenv("AGENTKIT_TEST_API_KEY", "should-be-stripped")

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Workspace: ws,
		SpillDir:  spillDir,
		Env:       []string{"PATH=" + os.Getenv("PATH"), "MARK04=1"},
		Ignore:    NoGlobalExcludes(),
	}

	all, err := All(opts)
	if err != nil {
		t.Fatal(err)
	}

	toolByName := func(name string) core.Tool {
		for _, tl := range all {
			if tl.Name == name {
				return tl
			}
		}
		t.Fatalf("%s not found in tool set", name)
		return core.Tool{}
	}

	bigSize := DefaultByteLimit + 1000
	payload := strings.Repeat("X", bigSize-4) + "END\n"
	payloadFile := filepath.Join(root, "payload.txt")
	if err := os.WriteFile(payloadFile, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}

	return all, toolByName, payloadFile, bigSize, spillDir
}

func TestTS_04_28_ShellToolsRunWithExplicitEnvTailTruncationAndSpillDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses unix shell commands")
	}

	_, toolByName, payloadFile, bigSize, spillDir := ts0428Setup(t)

	for _, name := range []string{"execute", "run_command"} {
		t.Run(name+"_env", func(t *testing.T) {
			var envArgs json.RawMessage
			if name == "execute" {
				envArgs = json.RawMessage(`{"command":"env","timeout_s":5}`)
			} else {
				envArgs = json.RawMessage(`{"argv":["env"],"timeout_s":5}`)
			}
			res := toolByName(name).Execute(context.Background(), envArgs)
			if !res.OK {
				t.Fatalf("%s env failed: %+v", name, res)
			}
			envOut := res.Text
			if !strings.Contains(envOut, "MARK04=1") {
				t.Fatalf("%s: MARK04=1 not in env output:\n%s", name, envOut)
			}
			if strings.Contains(envOut, "AGENTKIT_TEST_API_KEY") {
				t.Fatalf("%s: AGENTKIT_TEST_API_KEY leaked into env output:\n%s", name, envOut)
			}
			// The explicit Env from Options must be used, not the runner's
			// nil-Env default. PATH must be present.
			if !strings.Contains(envOut, "PATH=") {
				t.Fatalf("%s: PATH not in env output:\n%s", name, envOut)
			}
		})

		t.Run(name+"_truncation", func(t *testing.T) {
			ts0428CheckTruncation(t, name, toolByName(name), payloadFile, bigSize, spillDir)
		})
	}
}

// ts0428CheckTruncation verifies tail truncation, SpillDir usage and metadata
// for a single shell tool.
func ts0428CheckTruncation(t *testing.T, name string, tool core.Tool, payloadFile string, bigSize int, spillDir string) {
	t.Helper()
	var bigArgs json.RawMessage
	if name == "execute" {
		bigArgs = json.RawMessage(fmt.Sprintf(`{"command":"cat %s","timeout_s":10}`, payloadFile))
	} else {
		bigArgs = json.RawMessage(fmt.Sprintf(`{"argv":["cat","%s"],"timeout_s":10}`, payloadFile))
	}
	res := tool.Execute(context.Background(), bigArgs)

	// Tail-mode marker at the top proves tail truncation.
	wantPrefix := fmt.Sprintf("[1000 bytes elided of %d total. Full output: ", bigSize)
	if !strings.HasPrefix(res.Text, wantPrefix) {
		t.Fatalf("%s: Text prefix = %q, want %q", name,
			res.Text[:min(len(res.Text), len(wantPrefix)+20)], wantPrefix)
	}
	if !strings.HasSuffix(res.Text, "END\n") {
		t.Fatalf("%s: Text does not end with END\\n", name)
	}

	markerEnd := strings.Index(res.Text, "]\n")
	if markerEnd < 0 {
		t.Fatalf("%s: cannot find end of marker line", name)
	}
	retained := res.Text[markerEnd+2:]
	if len(retained) != DefaultByteLimit {
		t.Fatalf("%s: retained = %d bytes, want %d", name, len(retained), DefaultByteLimit)
	}

	if res.Metadata == nil {
		t.Fatalf("%s: Metadata is nil", name)
	}
	sp := res.Metadata.SpillPath
	if !strings.HasPrefix(sp, spillDir) {
		t.Fatalf("%s: SpillPath %q not under %q", name, sp, spillDir)
	}
	base := filepath.Base(sp)
	if !strings.HasPrefix(base, "agentkit-exec-") || !strings.HasSuffix(base, ".log") {
		t.Fatalf("%s: SpillPath base %q does not match agentkit-exec-*.log", name, base)
	}
	spillData, err := os.ReadFile(sp)
	if err != nil {
		t.Fatalf("%s: read spill: %v", name, err)
	}
	if len(spillData) != bigSize {
		t.Fatalf("%s: spill = %d bytes, want %d", name, len(spillData), bigSize)
	}
	if !res.Metadata.Truncated {
		t.Fatalf("%s: Truncated = false", name)
	}
	if res.Metadata.TotalBytes != int64(bigSize) {
		t.Fatalf("%s: TotalBytes = %d, want %d", name, res.Metadata.TotalBytes, bigSize)
	}
	if res.Metadata.Outcome != "ok" {
		t.Fatalf("%s: Outcome = %q, want ok", name, res.Metadata.Outcome)
	}
}

// ------------------------------------------------------------------ TS-04-29
// A model command that reads stdin through the shell tools sees end-of-file
// at once.

func TestTS_04_29_ShellToolStdinSeesEOFAtOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses unix shell commands")
	}

	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Workspace: ws,
		Env:       []string{"PATH=" + os.Getenv("PATH")},
		Ignore:    NoGlobalExcludes(),
	}
	all, err := All(opts)
	if err != nil {
		t.Fatal(err)
	}
	toolByName := func(name string) core.Tool {
		for _, tl := range all {
			if tl.Name == name {
				return tl
			}
		}
		t.Fatalf("%s not found in tool set", name)
		return core.Tool{}
	}

	// execute: 'cat; echo after'
	t.Run("execute", func(t *testing.T) {
		res := toolByName("execute").Execute(context.Background(),
			json.RawMessage(`{"command":"cat; echo after","timeout_s":5}`))
		if !res.OK {
			t.Fatalf("execute failed: %+v", res)
		}
		// cat with null-device stdin produces nothing; echo produces "after\n".
		if strings.TrimSpace(res.Text) != "after" {
			t.Fatalf("execute: Text = %q, want %q", res.Text, "after\n")
		}
		if res.Metadata == nil || res.Metadata.Outcome != "ok" {
			t.Fatalf("execute: Metadata.Outcome = %v, want ok", res.Metadata)
		}
		if res.Metadata.DurationMS >= 2000 {
			t.Fatalf("execute: DurationMS = %d, command blocked on stdin", res.Metadata.DurationMS)
		}
		// A clean exit (0) must not produce a status line.
		if strings.Contains(res.Text, "[exit") || strings.Contains(res.Text, "[timeout") {
			t.Fatalf("execute: unexpected status line in Text: %q", res.Text)
		}
	})

	// run_command: ['sh','-c','cat; echo after']
	t.Run("run_command", func(t *testing.T) {
		res := toolByName("run_command").Execute(context.Background(),
			json.RawMessage(`{"argv":["sh","-c","cat; echo after"],"timeout_s":5}`))
		if !res.OK {
			t.Fatalf("run_command failed: %+v", res)
		}
		if strings.TrimSpace(res.Text) != "after" {
			t.Fatalf("run_command: Text = %q, want %q", res.Text, "after\n")
		}
		if res.Metadata == nil || res.Metadata.Outcome != "ok" {
			t.Fatalf("run_command: Metadata.Outcome = %v, want ok", res.Metadata)
		}
		if res.Metadata.DurationMS >= 2000 {
			t.Fatalf("run_command: DurationMS = %d, command blocked on stdin", res.Metadata.DurationMS)
		}
		if strings.Contains(res.Text, "[exit") || strings.Contains(res.Text, "[timeout") {
			t.Fatalf("run_command: unexpected status line in Text: %q", res.Text)
		}
	})
}

// ------------------------------------------------------------------ TS-04-30
// execResultToTool produces the same Text, Data, Error and Metadata as the
// pre-change function for any ExecResult.

func TestTS_04_30_ExecResultToToolMatchesPreChange(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	outcomes := []Outcome{OutcomeOK, OutcomeExit, OutcomeSignal, OutcomeTimeout, OutcomeAbort}
	exitCodes := []int{0, 1, 2, 3, 137, 143, -1}
	timeouts := []time.Duration{0, 30 * time.Second}
	outputs := []string{
		"",
		"hello",
		"hello\n",
		"line1\nline2\n",
		"no trailing newline",
	}
	spillPaths := []string{"", "/tmp/spill/agentkit-exec-12345.log"}

	for i := 0; i < 500; i++ {
		res := ExecResult{
			Output:     outputs[rng.Intn(len(outputs))],
			Outcome:    outcomes[rng.Intn(len(outcomes))],
			ExitCode:   exitCodes[rng.Intn(len(exitCodes))],
			Truncated:  rng.Intn(2) == 1,
			TotalBytes: int64(rng.Intn(200000)),
			SpillPath:  spillPaths[rng.Intn(len(spillPaths))],
			Duration:   time.Duration(rng.Intn(60000)) * time.Millisecond,
		}
		timeout := timeouts[rng.Intn(len(timeouts))]

		got := execResultToTool(res, timeout)
		want := preChangeExecResultToTool(res, timeout)

		if got.OK != want.OK {
			t.Fatalf("iteration %d: OK = %v, want %v (res=%+v, timeout=%v)", i, got.OK, want.OK, res, timeout)
		}
		if got.Text != want.Text {
			t.Fatalf("iteration %d: Text = %q, want %q (res=%+v, timeout=%v)", i, got.Text, want.Text, res, timeout)
		}
		if got.Error != want.Error {
			t.Fatalf("iteration %d: Error = %q, want %q (res=%+v, timeout=%v)", i, got.Error, want.Error, res, timeout)
		}
		if !reflect.DeepEqual(got.Data, want.Data) {
			t.Fatalf("iteration %d: Data = %v, want %v (res=%+v, timeout=%v)", i, got.Data, want.Data, res, timeout)
		}
		// Compare Metadata by value, including ExitCode pointee.
		if got.Metadata == nil || want.Metadata == nil {
			t.Fatalf("iteration %d: Metadata nil: got=%v want=%v", i, got.Metadata, want.Metadata)
		}
		if got.Metadata.Truncated != want.Metadata.Truncated ||
			got.Metadata.TruncatedBy != want.Metadata.TruncatedBy ||
			got.Metadata.TotalBytes != want.Metadata.TotalBytes ||
			got.Metadata.SpillPath != want.Metadata.SpillPath ||
			got.Metadata.DurationMS != want.Metadata.DurationMS ||
			got.Metadata.Outcome != want.Metadata.Outcome {
			t.Fatalf("iteration %d: Metadata mismatch:\ngot:  %+v\nwant: %+v", i, *got.Metadata, *want.Metadata)
		}
		if (got.Metadata.ExitCode == nil) != (want.Metadata.ExitCode == nil) {
			t.Fatalf("iteration %d: ExitCode pointer nil mismatch: got=%v want=%v", i, got.Metadata.ExitCode, want.Metadata.ExitCode)
		}
		if got.Metadata.ExitCode != nil && *got.Metadata.ExitCode != *want.Metadata.ExitCode {
			t.Fatalf("iteration %d: ExitCode = %d, want %d", i, *got.Metadata.ExitCode, *want.Metadata.ExitCode)
		}
	}
}

// ------------------------------------------------------------------ TS-04-31
// An IOErr on the runner's result never reaches the shell tool's result.

func TestTS_04_31_IOErrNeverReachesShellToolResult(t *testing.T) {
	bases := []ExecResult{
		{
			Output:   "hello world\n",
			Outcome:  OutcomeOK,
			ExitCode: 0,
			Duration: 100 * time.Millisecond,
		},
		{
			Output:   "error output\n",
			Outcome:  OutcomeExit,
			ExitCode: 2,
			Duration: 200 * time.Millisecond,
		},
		{
			Output:     "truncated output tail",
			Outcome:    OutcomeTimeout,
			ExitCode:   137,
			Truncated:  true,
			TotalBytes: 100000,
			SpillPath:  "/tmp/spill/agentkit-exec-99999.log",
			Duration:   30 * time.Second,
		},
	}

	for i, base := range bases {
		t.Run(fmt.Sprintf("base_%d", i), func(t *testing.T) {
			timeout := 30 * time.Second

			// Without IOErr.
			a := execResultToTool(base, timeout)

			// With IOErr.
			withErr := base
			withErr.IOErr = errors.New("log write failed: disk full")
			b := execResultToTool(withErr, timeout)

			// Text must be equal.
			if a.Text != b.Text {
				t.Fatalf("Text differs:\n  without IOErr: %q\n  with IOErr:    %q", a.Text, b.Text)
			}
			// Text must not contain the error message.
			if strings.Contains(b.Text, "disk full") {
				t.Fatalf("Text contains IOErr message: %q", b.Text)
			}
			// Data must be equal.
			if !reflect.DeepEqual(a.Data, b.Data) {
				t.Fatalf("Data differs:\n  without IOErr: %v\n  with IOErr:    %v", a.Data, b.Data)
			}
			// Data must have exactly the keys output, exit_code, outcome.
			dataMap := b.Data
			wantKeys := map[string]bool{"output": true, "exit_code": true, "outcome": true}
			for k := range dataMap {
				if !wantKeys[k] {
					t.Fatalf("unexpected key in Data: %q", k)
				}
			}
			if len(dataMap) != len(wantKeys) {
				t.Fatalf("Data has %d keys, want %d", len(dataMap), len(wantKeys))
			}
			// No "io_err" key in Data.
			if _, ok := dataMap["io_err"]; ok {
				t.Fatal("Data contains io_err key; IOErr must not be rendered")
			}
			// Error must be equal.
			if a.Error != b.Error {
				t.Fatalf("Error differs: %q vs %q", a.Error, b.Error)
			}
			// Metadata must be equal.
			if a.Metadata == nil || b.Metadata == nil {
				t.Fatalf("Metadata nil: a=%v b=%v", a.Metadata, b.Metadata)
			}
			if !reflect.DeepEqual(*a.Metadata, *b.Metadata) {
				t.Fatalf("Metadata differs:\n  without IOErr: %+v\n  with IOErr:    %+v", *a.Metadata, *b.Metadata)
			}
		})
	}
}
