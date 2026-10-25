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

// isDiag reports whether argv is the missing-agent diagnostic.
func isDiag(argv []string) bool { return hasArgs(argv, "run") && argv[len(argv)-1] == "--version" }

// diagAnswer answers the diagnostic with res and err, anything else with 0.
func diagAnswer(res tools.ExecResult, err error) answerFunc {
	return func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if isDiag(argv) {
			return res, err
		}
		return exitResult(0, ""), nil
	}
}

// After the volume exists every failure runs the destroy step once, in order.
func TestCreateFailuresAreClassifiedAndCleanedUp(t *testing.T) {
	type starter = func(f *fakeRunner, cancel context.CancelFunc) (process, error)
	exit := func(code int, stderr string) starter {
		return func(f *fakeRunner, _ context.CancelFunc) (process, error) { return f.exitWith(code, stderr) }
	}
	for _, c := range []struct {
		name  string
		start starter
		diag  answerFunc
		want  any // an error the result Is, or a string it contains
	}{
		{"start fails", func(*fakeRunner, context.CancelFunc) (process, error) { return nil, errors.New("exec: denied") }, nil, "starting podman run: exec: denied"},
		{"protocol 99", func(f *fakeRunner, _ context.CancelFunc) (process, error) {
			fa := fakeagent.New()
			fa.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 99, Workdir: "/workspace"})
			return f.serveFakeAgent(fa)
		}, nil, sandbox.ErrProtocol},
		{"Landlock withheld", func(f *fakeRunner, _ context.CancelFunc) (process, error) {
			return f.serveAgent(false, sandbox.LandlockInfo{Note: "seccomp withheld Landlock"})
		}, nil, sandbox.ErrUnsupported},
		{"cancelled", func(f *fakeRunner, cancel context.CancelFunc) (process, error) {
			cancel()
			return f.serveAgent(false, sandbox.LandlockInfo{})
		}, nil, context.Canceled},
		{"engine error", exit(125, "Error: unknown flag\n"), nil, "podman run failed with exit status 125 (an engine error); engine stderr: Error: unknown flag"},
		{"agent exits 0", exit(0, "bye\n"), nil, "agent handshake"},
		{"agent fails", exit(1, "startup failed\n"), diagAnswer(exitResult(0, "agentkit-sandboxd v1.2.3\n"), nil), "engine stderr: startup failed"},
		{"diagnostic cannot run", exit(1, "boom\n"), diagAnswer(tools.ExecResult{}, errors.New("engine gone")), "engine stderr: boom"},
		{"no agent", exit(127, ""), diagAnswer(exitResult(126, "exec: permission denied"), nil), sandbox.ErrNoAgent},
	} {
		f := newFake(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.startFn = func() (process, error) { return c.start(f, cancel) }
		f.answer = c.diag
		_, err := testProvider(f, Options{}).Create(ctx, sandbox.Spec{Image: "img:1", RequireLandlock: c.name == "Landlock withheld"})
		cancel()
		want, isErr := c.want.(error)
		if err == nil || isErr && !errors.Is(err, want) || !isErr && (errors.Is(err, sandbox.ErrNoAgent) || !strings.Contains(err.Error(), c.want.(string))) {
			t.Errorf("%s: Create = %v, want %v", c.name, err, c.want)
		}
		if ran := slices.ContainsFunc(f.recorded(), func(c cmd) bool { return isDiag(c.argv) }); ran != (c.diag != nil) {
			t.Errorf("%s: the diagnostic ran %t", c.name, ran)
		}
		id := volumeID(f)
		i := f.index(0, "rm", "-f", "agentkit-"+id)
		j := f.index(i+1, "rm", "-f", "agentkit-"+id+"-init")
		if k := f.index(j+1, "volume", "rm", "agentkit-"+id); i < 0 || j < 0 || k < 0 || f.count("volume", "rm") != 1 || f.count("rm", "-f", "agentkit-"+id) != 1 {
			t.Errorf("%s: the destroy step did not run once, in order: %v", c.name, f.recorded())
		}
	}
}

// The diagnostic has no --init, -i or volume, so 126/127 are the runtime's own.
func TestCreateDiagnosesAnImageWithoutTheAgent(t *testing.T) {
	f := newFake(t)
	f.startFn = func() (process, error) { return f.exitWith(127, "") }
	f.answer = diagAnswer(exitResult(127, "exec: no such file or directory"), nil)
	_, err := testProvider(f, Options{Runtime: "krun", UserNS: "keep-id"}).Create(context.Background(), sandbox.Spec{Image: "missing:1"})
	if !errors.Is(err, sandbox.ErrNoAgent) {
		t.Fatalf("Create = %v, want ErrNoAgent", err)
	}
	for _, want := range []string{"missing:1", sandbox.AgentPath, "exec: no such file or directory", "FROM " + sandbox.BaseImageRepo + ":<version>", "make build-containers"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	d := f.recorded()[slices.IndexFunc(f.recorded(), func(c cmd) bool { return isDiag(c.argv) })]
	id, created := volumeID(f), createdAt(t, d.argv, time.Hour)
	want := []string{"run", "--rm", "--label", "agentkit.sandbox=" + id, "--label", "agentkit.sandbox.expires=" + expires(created, time.Hour),
		"--log-driver", "none", "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--runtime", "krun", "--userns=keep-id", "--entrypoint", sandbox.AgentPath, "missing:1", "--version"}
	if !slices.Equal(d.argv[1:], want) || d.timeout != 30*time.Second || d.deadline.IsZero() || time.Until(d.deadline) > 30*time.Second {
		t.Errorf("diagnostic %q with timeout %v, deadline %v;\nwant %q in its own 30-second context", d.argv[1:], d.timeout, d.deadline, want)
	}
}

func TestCreateStopsAtAFailingVolumeCreate(t *testing.T) {
	for want, res := range map[string]tools.ExecResult{
		"exit status 125: Error: volume driver unavailable": exitResult(125, "Error: volume driver unavailable\n"),
		"timeout: slow":   {Outcome: tools.OutcomeTimeout, Output: "slow"},
		"exec: not found": {},
	} {
		f := newFake(t)
		f.answer = func(context.Context, []string) (tools.ExecResult, error) {
			return res, map[bool]error{true: errors.New("exec: not found")}[res.Outcome == ""]
		}
		_, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{Image: "img:1"})
		if err == nil || !strings.Contains(err.Error(), "podman volume create") || !strings.Contains(err.Error(), want) || len(f.recorded()) != 1 {
			t.Errorf("Create = %v after %d commands, want %q and nothing more", err, len(f.recorded()), want)
		}
	}
}
