package podman

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// volumeID is the id of the volume the fake saw created.
func volumeID(t *testing.T, f *fakeRunner) string {
	t.Helper()
	for _, c := range f.recorded() {
		if hasArgs(c.argv, "volume", "create") {
			return strings.TrimPrefix(c.argv[len(c.argv)-1], "agentkit-")
		}
	}
	t.Fatal("no volume create was recorded")
	return ""
}

// diagnostic is the recorded diagnostic run, if any.
func diagnostic(f *fakeRunner) (cmd, bool) {
	for _, c := range f.recorded() {
		if !c.start && hasArgs(c.argv, "run") && c.argv[len(c.argv)-1] == "--version" {
			return c, true
		}
	}
	return cmd{}, false
}

// diagAnswer answers the diagnostic run with code and out, and every other
// short command with exit 0.
func diagAnswer(code int, out string) func(context.Context, []string) (tools.ExecResult, error) {
	return func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if hasArgs(argv, "run") && argv[len(argv)-1] == "--version" {
			return exitResult(code, out), nil
		}
		return exitResult(0, ""), nil
	}
}

// TS-17-32: protocol and RequireLandlock refusals pass through unchanged,
// and a done ctx surfaces ctx.Err(). 17-REQ-5.3.
func TestCreatePassThrough_TS17_32(t *testing.T) {
	ctx := context.Background()

	f := newFake(t)
	f.startFn = func([]string) (process, error) {
		fa := fakeagent.New()
		fa.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 99, Workdir: "/workspace"})
		return f.serveFakeAgent(fa)
	}
	_, err := testProvider(f, Options{}).Create(ctx, sandbox.Spec{Image: "img:1"})
	if !errors.Is(err, sandbox.ErrProtocol) {
		t.Errorf("protocol 99: Create = %v, want ErrProtocol", err)
	}
	if _, ok := diagnostic(f); ok {
		t.Error("protocol 99: a diagnostic ran")
	}
	if id := volumeID(t, f); !f.ran("volume", "rm", "agentkit-"+id) {
		t.Error("protocol 99: the volume was not removed")
	}

	f = newFake(t)
	f.startFn = func([]string) (process, error) {
		return f.serveAgentWith(false, sandbox.LandlockInfo{Note: "seccomp withheld Landlock"})
	}
	_, err = testProvider(f, Options{}).Create(ctx, sandbox.Spec{Image: "img:1", RequireLandlock: true})
	var se *sandbox.SpecError
	if !errors.As(err, &se) || se.Field != "RequireLandlock" || se.Provider != "podman" {
		t.Errorf("Landlock ABI 0: Create = %v, want the RequireLandlock SpecError from podman", err)
	}
	if id := volumeID(t, f); !f.ran("volume", "rm", "agentkit-"+id) {
		t.Error("Landlock ABI 0: the volume was not removed")
	}

	f = newFake(t)
	f.startFn = func([]string) (process, error) { return f.silent() }
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err = testProvider(f, Options{}).Create(cctx, sandbox.Spec{Image: "img:1"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: Create = %v, want context.Canceled", err)
	}
	if id := volumeID(t, f); !f.ran("volume", "rm", "agentkit-"+id) {
		t.Error("cancelled: the volume was not removed")
	}
}

// TS-17-33: an engine process exiting 125 gives an engine error with its
// stderr tail and no diagnostic. 17-REQ-5.4.
func TestCreateEngineError_TS17_33(t *testing.T) {
	f := newFake(t)
	f.startFn = func([]string) (process, error) { return f.exitWith(125, "Error: unknown flag: --unsetenv-all\n") }
	_, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1"})
	if err == nil || !strings.Contains(err.Error(), "podman") || !strings.Contains(err.Error(), "125") ||
		!strings.Contains(err.Error(), "unknown flag: --unsetenv-all") || errors.Is(err, sandbox.ErrNoAgent) {
		t.Fatalf("Create = %v, want an engine error naming podman and 125 with the stderr tail", err)
	}
	if _, ok := diagnostic(f); ok {
		t.Error("a diagnostic ran after exit 125")
	}
	if id := volumeID(t, f); !f.ran("volume", "rm", "agentkit-"+id) {
		t.Error("the volume was not removed")
	}
}

// TS-17-34: a diagnostic exit of 127 gives ErrNoAgent with AgentPath and
// the fix, and the diagnostic argv is exact. 17-REQ-5.5, 17-REQ-5.7.
func TestCreateNoAgent_TS17_34(t *testing.T) {
	f := newFake(t)
	f.startFn = func([]string) (process, error) { return f.exitWith(127, "") }
	f.answer = diagAnswer(127, "exec: no such file or directory")
	_, err := testProvider(f, Options{Runtime: "krun", UserNS: "keep-id"}).Create(context.Background(), sandbox.Spec{Image: "missing:1"})
	if !errors.Is(err, sandbox.ErrNoAgent) {
		t.Fatalf("Create = %v, want ErrNoAgent", err)
	}
	for _, want := range []string{"missing:1", sandbox.AgentPath, "exec: no such file or directory",
		"FROM " + sandbox.BaseImageRepo + ":<version>", "make build-containers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	d, ok := diagnostic(f)
	if !ok {
		t.Fatal("no diagnostic ran")
	}
	id := volumeID(t, f)
	created, perr := time.Parse(time.RFC3339, labelValue(d.argv, "agentkit.sandbox.expires"))
	if perr != nil {
		t.Fatal(perr)
	}
	want := []string{"run", "--rm",
		"--label", "agentkit.sandbox=" + id, "--label", "agentkit.sandbox.expires=" + created.Format(time.RFC3339),
		"--log-driver", "none", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--runtime", "krun", "--userns=keep-id", "--entrypoint", sandbox.AgentPath, "missing:1", "--version"}
	if !slices.Equal(d.argv[1:], want) {
		t.Errorf("diagnostic %q,\nwant %q", d.argv[1:], want)
	}
	if slices.Contains(d.argv, "--init") || slices.Contains(d.argv, "-i") || slices.Contains(d.argv, "--mount") {
		t.Errorf("diagnostic %q has --init, -i or --mount", d.argv)
	}
	if d.timeout <= 0 || d.timeout > 30*time.Second || d.deadline.IsZero() || time.Until(d.deadline) > 30*time.Second {
		t.Errorf("diagnostic timeout %v, deadline %v; want its own 30-second context", d.timeout, d.deadline)
	}
}

// TS-17-35: a diagnostic exit other than 126 or 127 returns the handshake
// error with the original stderr tail. 17-REQ-5.6.
func TestCreateHandshakeError_TS17_35(t *testing.T) {
	f := newFake(t)
	f.startFn = func([]string) (process, error) { return f.exitWith(1, "agentkit-sandboxd: startup failed\n") }
	f.answer = diagAnswer(0, "agentkit-sandboxd v1.2.3 (protocol 1.2)\n")
	_, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1"})
	if err == nil || !strings.Contains(err.Error(), "agent handshake") || !strings.Contains(err.Error(), "agentkit-sandboxd: startup failed") ||
		errors.Is(err, sandbox.ErrNoAgent) {
		t.Fatalf("Create = %v, want the handshake error with the stderr tail", err)
	}
	if _, ok := diagnostic(f); !ok {
		t.Error("no diagnostic ran after exit 1")
	}
}

// TS-17-36: every Create failure after volume create removes the container
// names and the volume, and a failing volume create runs nothing else.
// 17-REQ-5.8.
func TestCreateFailuresCleanUp_TS17_36(t *testing.T) {
	for _, mode := range []string{"start fails", "exit 125", "diagnostic 127", "diagnostic 0", "protocol 99", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newFake(t)
			ctx := context.Background()
			switch mode {
			case "start fails":
				f.startFn = func([]string) (process, error) { return nil, errors.New("exec: podman: permission denied") }
			case "exit 125":
				f.startFn = func([]string) (process, error) { return f.exitWith(125, "Error: pull failed") }
			case "diagnostic 127":
				f.startFn = func([]string) (process, error) { return f.exitWith(1, "") }
				f.answer = diagAnswer(127, "")
			case "diagnostic 0":
				f.startFn = func([]string) (process, error) { return f.exitWith(1, "") }
				f.answer = diagAnswer(0, "")
			case "protocol 99":
				f.startFn = func([]string) (process, error) {
					fa := fakeagent.New()
					fa.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 99})
					return f.serveFakeAgent(fa)
				}
			case "cancelled":
				f.startFn = func([]string) (process, error) { return f.silent() }
				cctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(100*time.Millisecond, cancel)
				ctx = cctx
			}
			if _, err := testProvider(f, Options{}).Create(ctx, sandbox.Spec{Image: "img:1"}); err == nil {
				t.Fatal("Create succeeded")
			}
			id := volumeID(t, f)
			i := f.index(0, "rm", "-f", "agentkit-"+id)
			j := f.index(i+1, "rm", "-f", "agentkit-"+id+"-init")
			k := f.index(j+1, "volume", "rm", "agentkit-"+id)
			if i < 0 || j < 0 || k < 0 {
				t.Fatalf("the destroy commands did not run in order: %v", f.recorded())
			}
			if f.count("volume", "rm") != 1 || f.count("rm", "-f", "agentkit-"+id) != 1 {
				t.Errorf("the destroy step ran more than once: %v", f.recorded())
			}
		})
	}
	f := newFake(t)
	f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if hasArgs(argv, "volume", "create") {
			return exitResult(125, "Error: volume driver unavailable"), nil
		}
		return exitResult(0, ""), nil
	}
	_, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1"})
	if err == nil || !strings.Contains(err.Error(), "volume driver unavailable") {
		t.Fatalf("failing volume create: Create = %v", err)
	}
	for _, c := range f.recorded() {
		if c.start || hasArgs(c.argv, "volume", "rm") {
			t.Errorf("a failing volume create was followed by %q", c.argv)
		}
	}
}
