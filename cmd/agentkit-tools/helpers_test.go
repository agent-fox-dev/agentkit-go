package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// binary is the command built once for the package's tests.
func binary(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentkit-tools-test-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "agentkit-tools")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		out, err := exec.Command("go", "build", "-buildvcs=false", "-o", binPath, ".").CombinedOutput()
		if err != nil {
			buildErr = errors.New(string(out))
		}
	})
	if buildErr != nil {
		t.Fatalf("building agentkit-tools: %v", buildErr)
	}
	return binPath
}

// TestMain removes the built binary after the package's tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if binPath != "" {
		_ = os.RemoveAll(filepath.Dir(binPath))
	}
	os.Exit(code)
}

// result is what one run of the command printed and its exit code.
type result struct {
	stdout, stderr string
	exit           int
}

// lines are the non-empty lines of s.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// testEnv is the test process's environment without provider credentials,
// with the given NAME=value entries added.
func testEnv(extra ...string) []string {
	var env []string
	for k, v := range testutil.Environ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "OPENROUTER_API_KEY") {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return append(env, extra...)
}

// runIn runs the built command in dir with env and args, bounded at a minute.
func runIn(t testing.TB, dir string, env []string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary(t), args...)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := result{stdout: stdout.String(), stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
	case err != nil:
		t.Fatalf("running agentkit-tools %v: %v", args, err)
	}
	return res
}

// run runs the built command with the test environment.
func run(t testing.TB, args ...string) result {
	t.Helper()
	return runIn(t, "", testEnv(), args...)
}

// inProcess runs the command's entry point in this process.
func inProcess(args ...string) result {
	var stdout, stderr bytes.Buffer
	code := runMain(args, &stdout, &stderr)
	return result{stdout: stdout.String(), stderr: stderr.String(), exit: code}
}

// sampleTree is a small Go project with the word needle in a.go.
func sampleTree(t testing.TB) string {
	t.Helper()
	return testutil.Tree(t, map[string]string{
		"go.mod": "module sample\n\ngo 1.27\n",
		"a.go":   "package sample\n\n// Needle is what the searches find: needle.\nfunc Needle() int { return 1 }\n",
		"b.go":   "package sample\n\nfunc Other() int { return 2 }\n",
		"c.go":   "package sample\n\n// Third mentions needle too.\nfunc Third() int { return 3 }\n",
		"README": "a sample project\n",
	})
}

// listing is the parsed output of `agentkit-tools tools`.
type listing []map[string]json.RawMessage

// names are the tool names of l, in order.
func (l listing) names() []string {
	var out []string
	for _, d := range l {
		var n string
		_ = json.Unmarshal(d["name"], &n)
		out = append(out, n)
	}
	return out
}

// get is the entry of l named name, or nil.
func (l listing) get(name string) map[string]json.RawMessage {
	for _, d := range l {
		var n string
		_ = json.Unmarshal(d["name"], &n)
		if n == name {
			return d
		}
	}
	return nil
}

// parseListing parses the listing on stdout, failing the test when it is not one.
func parseListing(t testing.TB, stdout string) listing {
	t.Helper()
	var l listing
	if err := json.Unmarshal([]byte(stdout), &l); err != nil {
		t.Fatalf("the listing is not a JSON array of objects: %v\n%s", err, stdout)
	}
	return l
}
