package podman

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-29: for any valid input, Args keeps every hardening flag, mounts
// exactly the workspace volume, ends with the agent and its arguments, and
// never emits a forbidden flag. 17-REQ-4.8.
func TestArgsProperty_TS17_29(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 29))
	word := func(alphabet string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		return string(b)
	}
	const lower = "abcdefghijklmnopqrstuvwxyz"
	workdirs := []string{"", "/workspace", "/home/agentkit/ws", "/srv/project", "/tmpfoo", "/w"}
	for range 2000 {
		o := Options{Engine: EnginePodman}
		if r.IntN(2) == 0 {
			o.Engine = EngineDocker
		} else if r.IntN(3) == 0 {
			o.Runtime = "krun"
		}
		if r.IntN(3) == 0 {
			o.UserNS = word(lower+"-:0123456789", 1+r.IntN(8))
		}
		id := word("0123456789abcdef", 16)
		created := time.Unix(r.Int64N(4e9), r.Int64N(1e9))
		spec := sandbox.Spec{
			Image:   word(lower+"0123456789./:-_", 1+r.IntN(20)),
			Workdir: workdirs[r.IntN(len(workdirs))],
			TTL:     time.Duration(r.Int64N(int64(48 * time.Hour))),
		}
		if r.IntN(2) == 0 {
			spec.Network.Mode = sandbox.NetworkUnrestricted
		}
		if r.IntN(2) == 0 {
			spec.Resources = sandbox.Resources{CPUs: float64(r.IntN(16)) / 2, MemoryBytes: r.Int64N(1 << 34), PIDs: r.Int64N(4096)}
		}
		for range r.IntN(4) {
			if spec.Labels == nil {
				spec.Labels = map[string]string{}
			}
			spec.Labels[word(lower+".-_", 1+r.IntN(10))] = word(lower+" =/", r.IntN(10))
		}
		delete(spec.Labels, "agentkit.sandbox")
		for k := range spec.Labels {
			if strings.HasPrefix(k, "agentkit.sandbox.") {
				delete(spec.Labels, k)
			}
		}
		for range r.IntN(4) {
			if spec.Env == nil {
				spec.Env = map[string]string{}
			}
			spec.Env[strings.ToUpper(word(lower+"_", 1+r.IntN(8)))] = word(lower+" =/:", r.IntN(12))
		}
		delete(spec.Env, "HOME")
		delete(spec.Env, "TMPDIR")
		eff, err := sandbox.CheckSpec(newResolved(o, "/usr/bin/podman", o.Engine, &fakeRunner{}).Capabilities(), spec)
		if err != nil {
			t.Fatalf("CheckSpec(%+v): %v", spec, err)
		}
		a := Args(o, id, created, eff)
		desc := fmt.Sprintf("Args(%+v, %s, %v, %+v) = %q", o, id, created, eff, a)
		for _, h := range [][]string{{"--init"}, {"--log-driver", "none"}, {"--cap-drop", "ALL"}, {"--security-opt", "no-new-privileges"},
			{"--read-only"}, {"--tmpfs", "/tmp:rw,exec,nosuid,nodev,mode=1777"}} {
			if len(without(a, h...)) == len(a) {
				t.Fatalf("%s lacks %q", desc, h)
			}
		}
		mounts := 0
		for i, e := range a {
			if e == "--mount" {
				mounts++
				if want := "type=volume,source=agentkit-" + id + ",target=" + eff.Workdir; i+1 >= len(a) || a[i+1] != want {
					t.Fatalf("%s mounts something other than %q", desc, want)
				}
			}
		}
		if mounts != 1 {
			t.Fatalf("%s has %d --mount flags", desc, mounts)
		}
		e := slices.Index(a, "--entrypoint")
		if e < 0 || a[e+1] != sandbox.AgentPath || a[e+2] != eff.Image || !slices.Equal(a[e+3:], sandbox.AgentArgs(eff)) {
			t.Fatalf("%s does not end with the agent, the image and AgentArgs", desc)
		}
		assertNoForbidden(t, a)
		if t.Failed() {
			t.Fatal(desc)
		}
	}
}
