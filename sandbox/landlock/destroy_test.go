package landlock

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// recordingTable records "session" on each scan of the fake table.
type recordingTable struct {
	*fakeTable
	order *[]string
}

func (r recordingTable) list() ([]procEntry, error) {
	*r.order = append(*r.order, "session")
	return r.fakeTable.list()
}

// Each action runs even after an earlier one failed, and their errors are joined.
func TestDestroyStopsKillsAndRemovesOnce(t *testing.T) {
	oldStop, oldSession := stopWait, sessionWait
	stopWait, sessionWait = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { stopWait, sessionWait = oldStop, oldSession })
	errKill, errScan := errors.New("kill failed"), errors.New("scan failed")
	for _, failing := range []bool{false, true} {
		var order []string
		ft, dir := newFakeTable(), t.TempDir()
		h := newAgentProc(7, nil)
		h.killFn = func() error { order = append(order, "kill"); h.setExit(-1, true, 9); return errKill }
		if !failing {
			h.setExit(0, false, 0) // the agent exited on EOF
		} else {
			ft.listErr = errScan
		}
		remove := func(d string) error { order = append(order, "remove"); return removeTree(d) }
		destroy := newDestroy(h, recordingTable{ft, &order}, dir, remove)
		err := destroy(context.Background())
		want := []string{"session", "remove"}
		if failing {
			want = []string{"kill", "session", "remove"}
		}
		if again := destroy(context.Background()); !slices.Equal(slices.Compact(order), want) || again != nil ||
			failing != (errors.Is(err, errKill) && errors.Is(err, errScan)) || !failing && err != nil {
			t.Errorf("failing %t: %v then %v, order %v; want %v once", failing, err, again, order, want)
		}
		if _, serr := os.Stat(dir); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("failing %t: the directory remains", failing)
		}
	}
}
