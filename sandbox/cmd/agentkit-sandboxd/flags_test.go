package main

import (
	"flag"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-15-15: parseFlags accepts --network with default none and repeatable
// --landlock-ro and --landlock-rw in order, and defines no flag that
// disables confinement. 15-REQ-3.1.
func TestLandlockFlags_TS15_15(t *testing.T) {
	c, err := parseFlags(nil)
	if err != nil || c.network != sandbox.NetworkNone {
		t.Errorf("no --network: %v, %v", c.network, err)
	}
	for s, m := range map[string]sandbox.NetworkMode{"allowlist": sandbox.NetworkAllowlist, "unrestricted": sandbox.NetworkUnrestricted, "none": sandbox.NetworkNone} {
		if c, err := parseFlags([]string{"--network", s}); err != nil || c.network != m {
			t.Errorf("--network %s: %v, %v", s, c.network, err)
		}
	}
	c, err = parseFlags([]string{"--landlock-ro", "/a", "--landlock-rw", "/x", "--landlock-ro", "/b", "--landlock-rw", "/y"})
	if err != nil || !slices.Equal(c.landlockRO, []string{"/a", "/b"}) || !slices.Equal(c.landlockRW, []string{"/x", "/y"}) {
		t.Errorf("ro %v, rw %v, %v", c.landlockRO, c.landlockRW, err)
	}
	var names []string
	flagSet(&config{}).VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	slices.Sort(names)
	if want := []string{"landlock-ro", "landlock-rw", "network", "ttl", "version", "workdir"}; !slices.Equal(names, want) {
		t.Errorf("flags %v, want exactly %v", names, want)
	}
}

// TS-15-20: for any effective spec, AgentArgs produces flags that parseFlags
// reads back as the spec's TTL, network mode and workdir. 15-REQ-3.6.
func TestAgentArgsRoundTrip_TS15_20(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 20))
	modes := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted}
	base := t.TempDir()
	for range 500 {
		spec := sandbox.Spec{TTL: time.Second + time.Duration(r.Int64N(int64(48*time.Hour-time.Second))),
			Network: sandbox.Network{Mode: modes[r.IntN(3)]}}
		if r.IntN(2) == 0 {
			spec.Workdir = filepath.Join(base, "w", string(rune('a'+r.IntN(26))))
		}
		args := sandbox.AgentArgs(spec)
		c, err := parseFlags(args)
		if err != nil || c.ttl != spec.TTL || c.network != spec.Network.Mode {
			t.Fatalf("AgentArgs(%+v) = %q parsed as %+v, %v", spec, args, c, err)
		}
		wantWD := spec.Workdir
		if wantWD == "" {
			wantWD = "/workspace"
		}
		if c.workdir != wantWD {
			t.Fatalf("%q: workdir %q, want %q", args, c.workdir, wantWD)
		}
		i := slices.Index(args, "--ttl")
		j := slices.Index(args, "--network")
		if i < 0 || i+1 >= len(args) || args[i+1] != spec.TTL.String() || j < 0 || j+1 >= len(args) || args[j+1] != spec.Network.Mode.String() {
			t.Fatalf("%q lacks --ttl %s or --network %s", args, spec.TTL, spec.Network.Mode)
		}
		if slices.Contains(args, "--workdir") != (spec.Workdir != "") {
			t.Fatalf("%q: --workdir present %v with Workdir %q", args, slices.Contains(args, "--workdir"), spec.Workdir)
		}
	}
}
