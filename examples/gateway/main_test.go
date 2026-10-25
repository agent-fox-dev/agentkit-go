package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// Over a gateway it starts in process on a loopback port: a wrong token is
// refused, the agent works in the sandbox under the guard, src comes out,
// and once the sandbox is closed the gateway lists none and its directory is gone.
func TestTheOfflineRunWorksThroughTheGatewayAndLeavesNoSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LocalDir is refused on Windows")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp, dir := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var b strings.Builder
	if err := run(context.Background(), &b, io.Discard, dir); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backend local", "a wrong token: refused", "on gateway/local", "labels map[example:gateway], expires in 10m0s",
		"TODOs: 2", "src: main.go, util.go", "rm: blocked_by_policy", "exported src: main.go, util.go",
		"the gateway holds 0 sandboxes", "Gateway example completed successfully"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	for _, name := range []string{"main.go", "util.go"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Errorf("the exported %s is not in the output directory: %v", name, err)
		}
	}
	if ents, err := os.ReadDir(filepath.Join(tmp, "agentkit-sandbox-local")); len(ents) > 0 || (err != nil && !errors.Is(err, os.ErrNotExist)) {
		t.Errorf("%d sandbox directories remain after the gateway closed the sandbox (%v)", len(ents), err)
	}
}

func TestTheProgramRunsOffline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LocalDir is refused on Windows")
	}
	env := testutil.Environ("ANTHROPIC_API_KEY")
	env["TMPDIR"] = t.TempDir()
	if res := testutil.GoRun(t, "", ".", env); res.Exit != 0 || !strings.Contains(res.Stdout, "Gateway example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

// The token file is agentkit-gateway's format: the first line that is neither
// blank nor a comment, trimmed. --url without a usable one fails before dialling.
func TestTheRemoteTokenIsTheFirstOfTheTokenFile(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteTree(t, dir, map[string]string{
		"tokens": "# a comment\n\n  first-token-0123456789abcdefghijklmnop  \nsecond\n",
		"empty":  "# only a comment\n\n",
	})
	if got, err := firstToken(filepath.Join(dir, "tokens")); err != nil || got != "first-token-0123456789abcdefghijklmnop" {
		t.Errorf("firstToken = %q, %v", got, err)
	}
	for file, want := range map[string]string{"": "--url needs --token-file", "empty": "holds no token", "missing": "no such file"} {
		path := file
		if file != "" {
			path = filepath.Join(dir, file)
		}
		if err := runRemote(context.Background(), io.Discard, dir, "http://127.0.0.1:1", path, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("runRemote with token file %q = %v, want an error saying %q", file, err, want)
		}
	}
}
