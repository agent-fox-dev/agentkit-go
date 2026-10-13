package podman

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// TS-17-52: for any labelled set, Reap returns exactly the sorted distinct
// expired ids and removes every container before the first volume.
// 17-REQ-8.5.
func TestReapProperty_TS17_52(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 52))
	pool := []string{"aaaa", "bbbb", "cccc", "dddd", "eeee"}
	for trial := range 300 {
		var containers, volumes []engineEntry
		var want []string
		for i := range r.IntN(31) {
			id := pool[r.IntN(len(pool))]
			var exp string
			expired := false
			switch r.IntN(5) {
			case 0:
				exp, expired = rfc(reapT.Add(-time.Duration(1+r.IntN(1e6))*time.Second)), true
			case 1:
				exp = rfc(reapT.Add(time.Duration(1+r.IntN(1e6)) * time.Second))
			case 2:
				exp, expired = rfc(reapT), true
			case 3:
				exp = ""
			case 4:
				exp = "garbage"
			}
			e := engineEntry{name: fmt.Sprintf("agentkit-%s-%d", id, i), labels: labels(id, exp)}
			if r.IntN(2) == 0 {
				containers = append(containers, e)
			} else {
				volumes = append(volumes, e)
			}
			if expired {
				want = append(want, id)
			}
		}
		slices.Sort(want)
		want = slices.Compact(want)
		f := newFake(t)
		f.answer = listingAnswer(containers, volumes)
		p := testProvider(f, Options{})
		p.now = func() time.Time { return reapT }
		ids, err := p.Reap(context.Background())
		if err != nil || !slices.Equal(ids, want) && !(len(ids) == 0 && len(want) == 0) {
			t.Fatalf("trial %d: Reap = %v, %v; want %v", trial, ids, err, want)
		}
		lastRm, firstVol := -1, len(f.recorded())
		for i, c := range f.recorded() {
			if hasArgs(c.argv, "rm", "-f") {
				lastRm = i
			}
			if hasArgs(c.argv, "volume", "rm") && i < firstVol {
				firstVol = i
			}
		}
		if lastRm > firstVol {
			t.Fatalf("trial %d: an rm -f followed a volume rm: %v", trial, f.recorded())
		}
		for _, e := range append(slices.Clone(containers), volumes...) {
			exp := e.labels["agentkit.sandbox.expires"]
			parsed, perr := time.Parse(time.RFC3339, exp)
			if (perr != nil || parsed.After(reapT)) && (f.ran("rm", "-f", e.name) || f.ran("volume", "rm", e.name)) {
				t.Fatalf("trial %d: %s (expires %q) was removed", trial, e.name, exp)
			}
		}
	}
}
