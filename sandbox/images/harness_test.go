package images

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// The image-test harness: it starts an agent from an image with the
// container engine, attaches to it over the engine's stdio, and cleans up
// whatever a test started or built. The tests in this package are opt-in:
// make sandbox-images-test builds the images and sets the variables.

const skipHint = "; run make sandbox-images-test to build the images and run these tests"

// pickEngine is AGENTKIT_CONTAINER when it is set and found, else the first
// of podman and docker found.
func pickEngine(getenv func(string) string, lookPath func(string) (string, error)) (string, bool) {
	if e := getenv("AGENTKIT_CONTAINER"); e != "" {
		if _, err := lookPath(e); err != nil {
			return "", false
		}
		return e, true
	}
	for _, e := range []string{"podman", "docker"} {
		if _, err := lookPath(e); err == nil {
			return e, true
		}
	}
	return "", false
}

// requireImage skips t under -short, without a container engine, or
// without envVar, and otherwise returns the engine and envVar's image.
func requireImage(t *testing.T, envVar string) (engine, ref string) {
	t.Helper()
	if testing.Short() {
		t.Skip("image tests are skipped under -short" + skipHint)
	}
	engine, ok := pickEngine(os.Getenv, exec.LookPath)
	if !ok {
		t.Skip("no container engine (podman or docker) found" + skipHint)
	}
	ref = os.Getenv(envVar)
	if ref == "" {
		t.Skip(envVar + " is not set" + skipHint)
	}
	return engine, ref
}

// expectedVersion is the version the agent under test must report.
func expectedVersion(getenv func(string) string) string {
	if v := getenv("AGENTKIT_SANDBOX_IMAGE_VERSION"); v != "" {
		return v
	}
	return "(devel)"
}

// testSpec is the spec every harness agent is started with.
var testSpec = sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkNone}, TTL: 10 * time.Minute, Workdir: "/workspace"}

// randomHex is n random bytes in hex.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// runArgs is the engine command line that starts image's agent on stdio,
// the container's name and the spec it was started with.
func runArgs(engine, image string) ([]string, string, sandbox.Spec) {
	name := "agentkit-imgtest-" + randomHex(8)
	argv := append([]string{engine, "run", "--rm", "-i", "--name", name, "--network", "none",
		"--env", "HOME=/home/agentkit", "--env", "TMPDIR=/tmp", "--entrypoint", sandbox.AgentPath, image},
		sandbox.AgentArgs(testSpec)...)
	return argv, name, testSpec
}

// lockedBuffer collects the engine's stderr from its own goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// harness is one running agent and the sandbox attached to it.
type harness struct {
	engine, name string
	cmd          *exec.Cmd
	exited       chan struct{}
	stderr       *lockedBuffer
	sb           sandbox.Sandbox
	tools        []core.Tool
	once         sync.Once
}

// stop kills the engine's client process and removes the container.
func (h *harness) stop() {
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
		select {
		case <-h.exited:
		case <-time.After(10 * time.Second):
		}
	}
	_ = exec.Command(h.engine, "rm", "-f", h.name).Run()
}

// start runs image's agent and attaches to it. On failure it returns the
// harness, for cleanup, and an error carrying the image, the cause and the
// engine's stderr.
func start(ctx context.Context, engine, image string) (*harness, error) {
	argv, name, spec := runArgs(engine, image)
	h := &harness{engine: engine, name: name, exited: make(chan struct{}), stderr: &lockedBuffer{}}
	h.cmd = exec.Command(argv[0], argv[1:]...)
	h.cmd.Stderr = h.stderr
	stdin, err := h.cmd.StdinPipe()
	if err != nil {
		return h, err
	}
	stdout, err := h.cmd.StdoutPipe()
	if err != nil {
		return h, err
	}
	if err := h.cmd.Start(); err != nil {
		return h, fmt.Errorf("starting %s: %w", image, err)
	}
	go func() { _ = h.cmd.Wait(); close(h.exited) }()
	h.sb, err = attach.Dial(ctx, stdioconn.New(stdout, stdin), attach.Config{ID: name, Spec: spec, Provider: "imgtest",
		Destroy: func(context.Context) error { h.stop(); return nil }})
	if err != nil {
		select {
		case <-h.exited:
		case <-time.After(5 * time.Second):
		}
		h.cleanup()
		return h, fmt.Errorf("attaching to %s: %v; engine stderr: %s", image, err, h.stderr.String())
	}
	return h, nil
}

// cleanup closes the sandbox, then always removes the container and reaps
// the engine's client process. It runs once, however often it is called.
func (h *harness) cleanup() {
	h.once.Do(func() {
		if h.sb != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = h.sb.Close(ctx)
			cancel()
		}
		h.stop()
	})
}

// startT starts image's agent, fails t with start's error, and fetches the
// tools; it cleans up when t ends, logging the engine's stderr if t failed.
func startT(t *testing.T, engine, image string) *harness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	h, err := start(ctx, engine, image)
	t.Cleanup(func() {
		h.cleanup()
		if t.Failed() {
			t.Logf("engine stderr: %s", h.stderr.String())
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	h.tools, err = h.sb.Tools(ctx, sandbox.ToolOptions{})
	if err := checkTools(h.tools, err); err != nil {
		t.Fatalf("%v; engine stderr: %s", err, h.stderr.String())
	}
	return h
}

// callTool runs the proxy tool name with input.
func callTool(ctx context.Context, tools []core.Tool, name, input string) (core.ToolResult, error) {
	for _, tl := range tools {
		if tl.Name == name {
			return tl.Execute(ctx, []byte(input)), nil
		}
	}
	return core.ToolResult{}, fmt.Errorf("no proxy tool %s", name)
}

var rcLine = regexp.MustCompile(`(?m)^rc=(\d+)\s*$`)

// execIn runs command through the proxy execute tool, appending
// `; echo rc=$?`, and returns its exit code and output without the rc line.
func execIn(ctx context.Context, tools []core.Tool, command string) (int, string, error) {
	input, _ := json.Marshal(map[string]string{"command": command + "; echo rc=$?"})
	r, err := callTool(ctx, tools, "execute", string(input))
	if err != nil {
		return 0, "", err
	}
	if r.Error != "" {
		return 0, r.Text, fmt.Errorf("execute %q: %s %s", command, r.Error, r.Detail)
	}
	m := rcLine.FindAllStringSubmatchIndex(r.Text, -1)
	if len(m) == 0 {
		return 0, r.Text, fmt.Errorf("execute %q: no rc= line in %q", command, r.Text)
	}
	last := m[len(m)-1]
	rc, err := strconv.Atoi(r.Text[last[2]:last[3]])
	if err != nil {
		return 0, r.Text, fmt.Errorf("execute %q: bad rc= line in %q: %v", command, r.Text, err)
	}
	return rc, strings.TrimSpace(r.Text[:last[0]]), nil
}

// execVia binds execIn to a harness, for landlockCheck.
func execVia(h *harness) func(string) (int, error) {
	return func(c string) (int, error) {
		rc, _, err := execIn(context.Background(), h.tools, c)
		return rc, err
	}
}

// checkFacts compares the agent's version and protocol major with what the
// test expects, naming both values on a mismatch.
func checkFacts(info sandbox.AgentInfo, expected string) error {
	var errs []error
	if info.Version != expected {
		errs = append(errs, fmt.Errorf("the agent reports version %q, want %q", info.Version, expected))
	}
	if info.ProtocolMajor != sandbox.ProtocolMajor {
		errs = append(errs, fmt.Errorf("the agent speaks protocol major %d, the host %d", info.ProtocolMajor, sandbox.ProtocolMajor))
	}
	return errors.Join(errs...)
}

// checkTools fails a Tools error (an ErrToolMismatch among them) or an
// empty tool list.
func checkTools(ts []core.Tool, err error) error {
	if err != nil {
		return fmt.Errorf("Tools: %w", err)
	}
	if len(ts) == 0 {
		return errors.New("Tools returned no tools")
	}
	return nil
}

// landlockCheck probes the agent's ruleset through run: at ABI 2 or more,
// /var/tmp must be denied and HOME, /tmp and /workspace allowed; below it,
// the note is logged and nothing runs.
func landlockCheck(info sandbox.AgentInfo, run func(string) (int, error), logf func(string, ...any)) error {
	if info.Landlock.ABI < sandbox.LandlockMinABI {
		logf("Landlock ABI %d, no check: %s", info.Landlock.ABI, info.Landlock.Note)
		return nil
	}
	var errs []error
	if rc, err := run("touch /var/tmp/agentkit-probe"); err != nil {
		errs = append(errs, err)
	} else if rc == 0 {
		errs = append(errs, errors.New("touch /var/tmp/agentkit-probe succeeded under the agent's Landlock ruleset"))
	}
	if rc, err := run(`touch "$HOME/p" /tmp/p /workspace/p`); err != nil || rc != 0 {
		errs = append(errs, fmt.Errorf(`touch "$HOME/p" /tmp/p /workspace/p: exit %d, %v`, rc, err))
	}
	return errors.Join(errs...)
}

// runEngine runs the container engine and returns its stdout, stderr and
// exit code.
func runEngine(t *testing.T, engine string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(engine, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s %v: %v", engine, args, err)
	}
	return so.String(), se.String(), code
}

// buildDerived builds an image of FROM base plus OverlayCopyLine(overlay)
// and returns its reference and an idempotent cleanup that removes it, also
// registered with t.
func buildDerived(t *testing.T, engine, base, overlay string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM "+base+"\n"+sandbox.OverlayCopyLine(overlay)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := "agentkit-imgtest-derived-" + randomHex(8)
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = exec.Command(engine, "rmi", "-f", ref).Run() }) }
	t.Cleanup(cleanup)
	if so, se, code := runEngine(t, engine, "build", "-t", ref, "-f", filepath.Join(dir, "Containerfile"), dir); code != 0 {
		t.Fatalf("building the derived image: exit %d\n%s%s", code, so, se)
	}
	return ref, cleanup
}

// ---- tests ----

// TS-16-22: the image tests pick AGENTKIT_CONTAINER when set, otherwise
// podman, then docker, from PATH. 16-REQ-7.2.
func TestPickEngine_TS16_22(t *testing.T) {
	look := func(found ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, f := range found {
				if f == n {
					return "/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "AGENTKIT_CONTAINER" {
				return v
			}
			return ""
		}
	}
	for _, c := range []struct {
		env, want string
		found     []string
		ok        bool
	}{
		{"docker", "docker", []string{"podman", "docker"}, true},
		{"", "podman", []string{"podman", "docker"}, true},
		{"", "docker", []string{"docker"}, true},
		{"", "", nil, false},
		{"podman", "", []string{"docker"}, false},
	} {
		got, ok := pickEngine(env(c.env), look(c.found...))
		if got != c.want || ok != c.ok {
			t.Errorf("AGENTKIT_CONTAINER=%q, found %v: (%q, %v), want (%q, %v)", c.env, c.found, got, ok, c.want, c.ok)
		}
	}
}

// TS-16-23: the harness builds the exact run command line with a random
// container name and sandbox.AgentArgs of the test spec. 16-REQ-7.3.
func TestRunArgs_TS16_23(t *testing.T) {
	want := sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkNone}, TTL: 10 * time.Minute, Workdir: "/workspace"}
	nameRe := regexp.MustCompile(`^agentkit-imgtest-[0-9a-f]{8,}$`)
	var names []string
	for range 2 {
		argv, name, spec := runArgs("podman", "img:1")
		if spec.Network.Mode != want.Network.Mode || spec.TTL != want.TTL || spec.Workdir != want.Workdir {
			t.Errorf("spec %+v", spec)
		}
		exp := append([]string{"podman", "run", "--rm", "-i", "--name", name, "--network", "none", "--env", "HOME=/home/agentkit",
			"--env", "TMPDIR=/tmp", "--entrypoint", "/usr/local/libexec/agentkit-sandboxd", "img:1"}, sandbox.AgentArgs(want)...)
		if fmt.Sprint(argv) != fmt.Sprint(exp) {
			t.Errorf("argv %q, want %q", argv, exp)
		}
		if !nameRe.MatchString(name) {
			t.Errorf("name %q", name)
		}
		names = append(names, name)
	}
	if names[0] == names[1] {
		t.Error("two names are equal")
	}
}

// TS-16-24: the expected version defaults to (devel), and the agent-facts
// and Tools checks fail with both values in the message. 16-REQ-7.4.
func TestCommonChecks_TS16_24(t *testing.T) {
	getenv := func(v string) func(string) string {
		return func(k string) string {
			if k == "AGENTKIT_SANDBOX_IMAGE_VERSION" {
				return v
			}
			return ""
		}
	}
	if expectedVersion(getenv("")) != "(devel)" || expectedVersion(getenv("v0.6.0")) != "v0.6.0" {
		t.Errorf("expected versions %q, %q", expectedVersion(getenv("")), expectedVersion(getenv("v0.6.0")))
	}
	info := sandbox.AgentInfo{Version: "v0.6.0", ProtocolMajor: sandbox.ProtocolMajor}
	if err := checkFacts(info, "(devel)"); err == nil || !strings.Contains(err.Error(), "v0.6.0") || !strings.Contains(err.Error(), "(devel)") {
		t.Errorf("version mismatch: %v", err)
	}
	bad := info
	bad.ProtocolMajor++
	if err := checkFacts(bad, "v0.6.0"); err == nil || !strings.Contains(err.Error(), fmt.Sprint(bad.ProtocolMajor)) ||
		!strings.Contains(err.Error(), fmt.Sprint(sandbox.ProtocolMajor)) {
		t.Errorf("protocol mismatch: %v", err)
	}
	if err := checkFacts(info, "v0.6.0"); err != nil {
		t.Errorf("matching facts: %v", err)
	}
	if err := checkTools(nil, fmt.Errorf("wrapped: %w", sandbox.ErrToolMismatch)); err == nil || !strings.Contains(err.Error(), sandbox.ErrToolMismatch.Error()) {
		t.Errorf("ErrToolMismatch: %v", err)
	}
	if err := checkTools(nil, nil); err == nil {
		t.Error("an empty tool list passed")
	}
}

// TS-16-25: below Landlock ABI 2 the Landlock check logs the note and runs
// no probe; at ABI 2 it runs exactly the two probes. 16-REQ-7.7.
func TestLandlockCheck_TS16_25(t *testing.T) {
	for _, l := range []sandbox.LandlockInfo{{ABI: 0, Note: "landlock unavailable"}, {ABI: 1, Note: "abi 1"}} {
		var ran, logged []string
		err := landlockCheck(sandbox.AgentInfo{Landlock: l}, func(c string) (int, error) { ran = append(ran, c); return 0, nil },
			func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
		if err != nil || len(ran) != 0 || !strings.Contains(strings.Join(logged, " "), l.Note) {
			t.Errorf("ABI %d: err %v, ran %q, logged %q", l.ABI, err, ran, logged)
		}
	}
	for _, varTmp := range []int{1, 0} {
		var ran []string
		codes := []int{varTmp, 0}
		err := landlockCheck(sandbox.AgentInfo{Landlock: sandbox.LandlockInfo{ABI: 2}}, func(c string) (int, error) {
			ran = append(ran, c)
			return codes[len(ran)-1], nil
		}, func(string, ...any) {})
		if want := []string{"touch /var/tmp/agentkit-probe", `touch "$HOME/p" /tmp/p /workspace/p`}; fmt.Sprint(ran) != fmt.Sprint(want) {
			t.Errorf("ran %q, want %q", ran, want)
		}
		if varTmp == 1 && err != nil {
			t.Errorf("a denied /var/tmp failed the check: %v", err)
		}
		if varTmp == 0 && (err == nil || !strings.Contains(err.Error(), "/var/tmp")) {
			t.Errorf("an allowed /var/tmp: %v, want a failure naming /var/tmp", err)
		}
	}
}

// TS-16-47: when the container cannot start, the harness reports the
// engine's stderr and leaves no container behind. 16-REQ-7.5.
func TestHarnessStartFailure_TS16_47(t *testing.T) {
	engine, _ := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	h, err := start(context.Background(), engine, "agentkit-imgtest-INVALID:Ref")
	if err == nil {
		t.Errorf("start succeeded, want an error carrying the engine's stderr")
	} else if _, stderr, ok := strings.Cut(err.Error(), "engine stderr: "); !ok || !strings.Contains(stderr, "agentkit-imgtest-INVALID") {
		t.Errorf("start = %v, want the engine's stderr, naming agentkit-imgtest-INVALID", err)
	}
	if h == nil {
		t.Fatal("start returned no harness to clean up")
	}
	h.cleanup()
	h.cleanup()
	if out, _, _ := runEngine(t, engine, "ps", "-a", "--filter", "name="+h.name, "--format", "{{.Names}}"); strings.TrimSpace(out) != "" {
		t.Errorf("a container remains: %q", out)
	}
}

// TS-16-48: harness cleanup closes the sandbox and removes the container and
// every image the test built. 16-REQ-7.6.
func TestHarnessCleanup_TS16_48(t *testing.T) {
	engine, base := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	_, overlay := requireImage(t, "AGENTKIT_SANDBOX_OVERLAY_IMAGE")
	h := startT(t, engine, base)
	h.cleanup()
	if _, _, err := execIn(context.Background(), h.tools, "true"); err == nil {
		t.Error("a proxy call on the closed sandbox succeeded")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _, _ := runEngine(t, engine, "ps", "-a", "--filter", "name="+h.name, "--format", "{{.Names}}")
		if strings.TrimSpace(out) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the container %s remains after 10s", h.name)
		}
		time.Sleep(200 * time.Millisecond)
	}
	ref, cleanup := buildDerived(t, engine, base, overlay)
	cleanup()
	if _, _, code := runEngine(t, engine, "image", "inspect", ref); code == 0 {
		t.Errorf("the derived image %s remains", ref)
	}
	for _, img := range []string{base, overlay} {
		if _, _, code := runEngine(t, engine, "image", "inspect", img); code != 0 {
			t.Errorf("the image under test %s is gone", img)
		}
	}
}
