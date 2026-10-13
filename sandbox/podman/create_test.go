package podman

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-16: NetworkAllowlist and DiskBytes are refused through CheckSpec
// with the engine as provider, before any command. 17-REQ-3.1.
func TestCreateRefusesUncovered_TS17_16(t *testing.T) {
	for _, engine := range []Engine{EnginePodman, EngineDocker} {
		for _, c := range []struct {
			spec  sandbox.Spec
			field string
		}{
			{sandbox.Spec{Image: "img:1", Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "example.com", Port: 443}}}}, "Network.Mode"},
			{sandbox.Spec{Image: "img:1", Resources: sandbox.Resources{DiskBytes: 1 << 30}}, "Resources.DiskBytes"},
		} {
			f := newFake(t)
			p := testProvider(f, Options{Engine: engine})
			_, err := p.Create(context.Background(), c.spec)
			var se *sandbox.SpecError
			if !errors.As(err, &se) || se.Field != c.field || se.Provider != string(engine) || !errors.Is(err, sandbox.ErrUnsupported) {
				t.Fatalf("%s: Create = %v, want a SpecError on %s from %s", engine, err, c.field, engine)
			}
			if _, want := sandbox.CheckSpec(p.Capabilities(), c.spec); err.Error() != want.Error() {
				t.Errorf("%s: Create = %q, CheckSpec = %q", engine, err, want)
			}
			if n := len(f.recorded()); n != 0 {
				t.Errorf("%s: %d commands ran", engine, n)
			}
		}
	}
}

// TS-17-22: with an empty Spec.Image the default image is run and reported
// in the effective spec. 17-REQ-3.7.
func TestCreateDefaultImage_TS17_22(t *testing.T) {
	f := newFake(t)
	p := testProvider(f, Options{})
	const img = "ghcr.io/agent-fox-dev/agentkit-sandbox:1.2.3"
	p.defaultImage = func() (string, error) { return img, nil }
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	run := f.started().argv
	i := slices.Index(run, sandbox.AgentPath)
	if i < 0 || run[i+1] != img || s.Spec().Image != img {
		t.Fatalf("run %q, Spec().Image %q; want %s after the agent path and reported", run, s.Spec().Image, img)
	}
}

// TS-17-23: in a (devel) build an empty Spec.Image returns
// ErrNoPublishedImage with no engine command. 17-REQ-3.8.
func TestCreateNoPublishedImage_TS17_23(t *testing.T) {
	f := newFake(t)
	_, err := testProvider(f, Options{}).Create(context.Background(), sandbox.Spec{})
	if !errors.Is(err, sandbox.ErrNoPublishedImage) || !strings.Contains(err.Error(), "Spec.Image") {
		t.Fatalf("Create = %v, want ErrNoPublishedImage saying to set Spec.Image", err)
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("%d commands ran", n)
	}
}

// labelValue is the value of the --label key=... in argv.
func labelValue(argv []string, key string) string {
	for i, e := range argv {
		if i > 0 && argv[i-1] == "--label" && strings.HasPrefix(e, key+"=") {
			return strings.TrimPrefix(e, key+"=")
		}
	}
	return ""
}

// TS-17-30: Create runs volume create before run, runs exactly Args,
// attaches through Hello and labels for both TTL sides. 17-REQ-5.1,
// 17-REQ-6.7.
func TestCreateSequence_TS17_30(t *testing.T) {
	f := newFake(t)
	o := Options{Engine: EnginePodman}
	p := testProvider(f, o)
	spec := sandbox.Spec{Image: "img:1", Labels: map[string]string{"team": "core"}, TTL: 10 * time.Minute}
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	id := s.ID()
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(id) || s.Agent().ProtocolMajor != sandbox.ProtocolMajor {
		t.Fatalf("ID %q, protocol major %d", id, s.Agent().ProtocolMajor)
	}
	cmds := f.recorded()
	if len(cmds) < 2 || cmds[0].start || !cmds[1].start {
		t.Fatalf("commands %v: want volume create, then the engine process", cmds)
	}
	vc := cmds[0].argv
	exp, err := time.Parse(time.RFC3339, labelValue(vc, "agentkit.sandbox.expires"))
	if err != nil {
		t.Fatalf("volume create %q: %v", vc, err)
	}
	created := exp.Add(-10 * time.Minute)
	want := append([]string{"volume", "create"}, append(labelArgs(id, created, sandbox.Spec{TTL: 10 * time.Minute, Labels: spec.Labels}), "agentkit-"+id)...)
	if !slices.Equal(vc[1:], want) {
		t.Errorf("volume create %q, want %q", vc[1:], want)
	}
	eff, err := sandbox.CheckSpec(p.Capabilities(), spec)
	if err != nil {
		t.Fatal(err)
	}
	run := cmds[1].argv
	if !slices.Equal(run[1:], Args(o, id, created, eff)) {
		t.Errorf("run %q,\nwant %q", run[1:], Args(o, id, created, eff))
	}
	if !slices.Contains(run, "--rm") || len(without(run, "--ttl", "10m0s")) == len(run) {
		t.Errorf("run %q lacks --rm or --ttl 10m0s", run)
	}
}
