package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// parseEnvOutput parses the output of the `env` command into a map of
// KEY→VALUE. Lines without '=' are appended to the previous entry's value
// (multiline env vars).
func parseEnvOutput(output string) map[string]string {
	result := make(map[string]string)
	var lastKey string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && !strings.Contains(k, " ") {
			result[k] = v
			lastKey = k
		} else if lastKey != "" {
			result[lastKey] += "\n" + line
		}
	}
	return result
}

// TS-04-17: A nil Env runs the child with ReducedEnv(nil): credentials are
// stripped and PATH and HOME are kept verbatim.
func TestTS_04_17_NilEnvRunsWithReducedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses env and sh -c")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}

	t.Setenv("AGENTKIT_TEST_API_KEY", "sekrit")
	t.Setenv("MY_SERVICE_TOKEN", "tok")

	ctx := context.Background()

	// RunArgv with nil Env.
	resArgv, err := RunArgv(ctx, []string{"env"}, ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}

	// Run with nil Env.
	resRun, err := Run(ctx, "env", ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for name, res := range map[string]ExecResult{"RunArgv": resArgv, "Run": resRun} {
		lines := strings.Split(strings.TrimRight(res.Output, "\n"), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "AGENTKIT_TEST_API_KEY=") {
				t.Errorf("%s: output contains AGENTKIT_TEST_API_KEY", name)
			}
			if strings.HasPrefix(line, "MY_SERVICE_TOKEN=") {
				t.Errorf("%s: output contains MY_SERVICE_TOKEN", name)
			}
		}
		wantPath := "PATH=" + os.Getenv("PATH")
		wantHome := "HOME=" + os.Getenv("HOME")
		if !strings.Contains(res.Output, wantPath) {
			t.Errorf("%s: output missing %q", name, wantPath)
		}
		if !strings.Contains(res.Output, wantHome) {
			t.Errorf("%s: output missing %q", name, wantHome)
		}
	}

	// The RunArgv output's variables equal ReducedEnv(nil) as a set.
	// Parse the output as KEY=VALUE pairs (the first '=' splits key from
	// value, so multiline values are handled by collecting lines that do
	// not contain '=' into the previous entry).
	argvVars := parseEnvOutput(resArgv.Output)
	reduced := ReducedEnv(nil)
	reducedSet := make(map[string]string, len(reduced))
	for _, kv := range reduced {
		k, v, _ := strings.Cut(kv, "=")
		reducedSet[k] = v
	}
	for k, v := range argvVars {
		rv, ok := reducedSet[k]
		if !ok {
			t.Errorf("child has %s=%q but ReducedEnv(nil) does not", k, v)
		} else if rv != v {
			t.Errorf("child %s=%q != ReducedEnv %s=%q", k, v, k, rv)
		}
	}
	for k := range reducedSet {
		if _, ok := argvVars[k]; !ok {
			t.Errorf("ReducedEnv(nil) has %s but child does not", k)
		}
	}
}

// TS-04-18: A non-nil Env, including an empty one, is passed verbatim and
// os.Environ() restores the full environment.
func TestTS_04_18_NonNilEnvPassedVerbatim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses env")
	}

	t.Setenv("AGENTKIT_TEST_API_KEY", "sekrit")
	t.Setenv("MY_SERVICE_TOKEN", "tok")

	ctx := context.Background()

	// With {"FOO=1"} the output is exactly "FOO=1\n".
	res, err := RunArgv(ctx, []string{"env"}, ExecOptions{
		Env:     []string{"FOO=1"},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunArgv with FOO=1: %v", err)
	}
	if res.Output != "FOO=1\n" {
		t.Fatalf("Output = %q, want %q", res.Output, "FOO=1\n")
	}

	// With the empty slice the output is empty.
	res, err = RunArgv(ctx, []string{"env"}, ExecOptions{
		Env:     []string{},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunArgv with empty Env: %v", err)
	}
	if res.Output != "" {
		t.Fatalf("Output = %q, want empty", res.Output)
	}

	// With os.Environ() the output contains the credential variables.
	res, err = RunArgv(ctx, []string{"env"}, ExecOptions{
		Env:     os.Environ(),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunArgv with os.Environ(): %v", err)
	}
	if !strings.Contains(res.Output, "AGENTKIT_TEST_API_KEY=sekrit") {
		t.Fatalf("Output missing AGENTKIT_TEST_API_KEY=sekrit")
	}
	if !strings.Contains(res.Output, "MY_SERVICE_TOKEN=tok") {
		t.Fatalf("Output missing MY_SERVICE_TOKEN=tok")
	}
}

// TS-04-19: RunArgv looks a bare name up through the PATH of the environment
// the child runs with, which for a nil Env is the process PATH.
func TestTS_04_19_RunArgvLooksUpBareNameThroughChildPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell scripts")
	}

	origPATH := os.Getenv("PATH")

	// Create two directories with different probe scripts.
	binA := t.TempDir()
	binB := t.TempDir()

	probeA := filepath.Join(binA, "agentkit-probe-04")
	probeB := filepath.Join(binB, "agentkit-probe-04")

	if err := os.WriteFile(probeA, []byte("#!/bin/sh\necho A\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(probeB, []byte("#!/bin/sh\necho B\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Put binA first on PATH.
	t.Setenv("PATH", binA+string(os.PathListSeparator)+origPATH)

	ctx := context.Background()

	// Nil Env: ReducedEnv(nil) keeps PATH verbatim, so the lookup uses binA.
	res, err := RunArgv(ctx, []string{"agentkit-probe-04"}, ExecOptions{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("RunArgv nil Env: %v", err)
	}
	if strings.TrimSpace(res.Output) != "A" {
		t.Fatalf("nil Env: Output = %q, want %q", res.Output, "A\n")
	}
	if res.Outcome != OutcomeOK {
		t.Fatalf("nil Env: Outcome = %q, want %q", res.Outcome, OutcomeOK)
	}

	// Explicit Env with binB first: the lookup uses the child's PATH.
	res, err = RunArgv(ctx, []string{"agentkit-probe-04"}, ExecOptions{
		Env:     []string{"PATH=" + binB + string(os.PathListSeparator) + origPATH},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunArgv explicit Env: %v", err)
	}
	if strings.TrimSpace(res.Output) != "B" {
		t.Fatalf("explicit Env: Output = %q, want %q", res.Output, "B\n")
	}
}
