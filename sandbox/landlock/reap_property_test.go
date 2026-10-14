package landlock

import (
	"math/rand/v2"
	"testing"
)

// TS-18-59: for any marker and process lookup the owner is alive exactly
// when the pid exists with the recorded start time on the recorded boot,
// and the decision is keep exactly then, whatever the role. 18-REQ-9.2.
func TestReapDecisionProperty_TS18_59(t *testing.T) {
	r := rand.New(rand.NewPCG(18, 59))
	boots := []string{"B1", "B2"}
	for range 2000 {
		m := marker{PID: r.IntN(3), StartTime: uint64(r.IntN(3)), BootID: boots[r.IntN(2)], Role: []string{"creator", "agent"}[r.IntN(2)]}
		l := lookupResult{present: r.IntN(2) == 0, start: uint64(r.IntN(3)), boot: boots[r.IntN(2)]}
		want := l.present && l.start == m.StartTime && l.boot == m.BootID
		if got := ownerAlive(m, l.present, l.start, l.boot); got != want {
			t.Fatalf("ownerAlive(%+v, %+v) = %t, want %t", m, l, got, want)
		}
		if got := decide(m, l); got != want {
			t.Fatalf("decide(%+v, %+v) = %t, want %t", m, l, got, want)
		}
	}
}
