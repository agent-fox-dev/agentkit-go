// Package testutil holds the test helpers that tests of many packages need and
// that need nothing of AgentKit: file trees, a git environment that ignores the
// machine's configuration, polling with a deadline, JSON, tar, golden files,
// the repository root, and running a program with `go run`.
//
// It imports only the standard library, so the tests of every package, the
// root's and the nested modules' alike, can use it. Test doubles over core (a
// scripted provider, an echo tool) are in internal/testkit.
package testutil

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// rootModule is the module path of the repository's root go.mod.
const rootModule = "github.com/agent-fox-dev/agentkit-go"

var update = flag.Bool("update", false, "rewrite golden files")

// RepoRoot is the repository's root directory: the nearest ancestor of the
// working directory whose go.mod declares the root module.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			strings.HasPrefix(string(raw), "module "+rootModule+"\n") {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod of %s above the working directory", rootModule)
		}
		dir = parent
	}
}

// WriteTree writes files, slash-separated paths relative to dir mapped to
// their contents, creating the directories they need.
func WriteTree(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Tree is a new temporary directory holding files, as WriteTree writes them.
func Tree(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	WriteTree(t, dir, files)
	return dir
}

// Hermetic points HOME, XDG_CONFIG_HOME and git's global and system
// configuration at empty places for the rest of the test, so that what git
// and the ignore rules read does not depend on whose machine runs it.
func Hermetic(t *testing.T) {
	t.Helper()
	cfg := t.TempDir()
	empty := filepath.Join(cfg, "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(cfg, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfg, "xdg"))
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// Within polls cond until it holds or d has passed, and reports whether it
// held.
func Within(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// Eventually fails t unless cond holds within five seconds; what names the
// condition in the failure.
func Eventually(t testing.TB, what string, cond func() bool) {
	t.Helper()
	if !Within(5*time.Second, cond) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// MustJSON is v encoded as JSON.
func MustJSON(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Object decodes b as a JSON object, numbers as json.Number, the form
// schema.Validate and the tools' Data take.
func Object(t testing.TB, b []byte) map[string]any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatalf("decoding a JSON object: %v\n%s", err, b)
	}
	return m
}

// SameJSON reports whether a and b are the same JSON value, whatever their
// spacing and key order.
func SameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Tar is a tar stream of regular files, mode 0644, in sorted name order.
func Tar(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		body := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Golden compares got with the file at path. Run with -update to rewrite the
// file instead; the diff is then reviewed, not blessed, since a golden
// regenerated from the output it checks proves nothing.
func Golden(t testing.TB, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v\n\nRun `go test -run %s -update` and review the diff.", err, t.Name())
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s differs from its golden.\n--- want ---\n%s\n--- got ---\n%s\n\n"+
			"If the change is intended, run `go test -run %s -update` and review the diff.",
			path, want, got, t.Name())
	}
}

// Environ is the test process's environment as a map, without the variables
// named in drop.
func Environ(drop ...string) map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if !slices.Contains(drop, name) {
			env[name] = value
		}
	}
	return env
}

// A Result is what a program run with GoRun printed and its exit code.
type Result struct {
	Stdout, Stderr string
	Exit           int
}

// GoRun runs `go run pkg` in dir (the test's directory when empty) with
// exactly the environment env, and returns what it printed and go run's exit
// code: 1 for a program that failed, whose own status go run reports on stderr
// as "exit status N". It skips the test when go is not on PATH.
func GoRun(t testing.TB, dir, pkg string, env map[string]string) Result {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	cmd := exec.Command(goBin, "run", pkg)
	cmd.Dir = dir
	cmd.Env = []string{}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	case err != nil:
		t.Fatalf("go run %s: %v", pkg, err)
	}
	return res
}
