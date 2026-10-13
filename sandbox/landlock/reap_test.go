package landlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sandboxWith writes a sandbox directory id under root whose marker names m.
func sandboxWith(t *testing.T, root, id string, m marker) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}
	if err := writeMarker(dir, m); err != nil {
		t.Fatal(err)
	}
	return dir
}

// reapProvider is a provider over table with the given removal.
func reapProvider(t *testing.T, table *fakeTable, mods ...func(*seams)) *provider {
	t.Helper()
	return newTestProvider(t, Options{}, append([]func(*seams){func(s *seams) { s.procs = table }}, mods...)...)
}

// TS-18-57: Reap considers only 16-hex directories with a readable marker
// and leaves every other entry untouched. 18-REQ-9.1.
func TestReapConsiders_TS18_57(t *testing.T) {
	ft := newFakeTable()
	p := reapProvider(t, ft)
	dead := marker{PID: 999, StartTime: 1, BootID: "B1", Role: "agent"}
	if err := os.WriteFile(filepath.Join(p.root, "0123456789abcdef"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sandboxWith(t, outside, "x", dead)
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(p.root, "fedcba9876543210")); err != nil {
		t.Fatal(err)
	}
	// ABCDEF0123456789 is uppercase hex; the spec's 0123456789ABCDEF would
	// collide with the file above on a case-insensitive filesystem.
	for _, n := range []string{"ABCDEF0123456789", "0123456789abcde", "notasandbox", "aaaaaaaaaaaaaaaa"} {
		if err := os.Mkdir(filepath.Join(p.root, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(p.root, "bbbbbbbbbbbbbbbb"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.root, "bbbbbbbbbbbbbbbb", markerName), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandboxWith(t, p.root, "cccccccccccccccc", dead)
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{"cccccccccccccccc"}) {
		t.Fatalf("Reap = %v, %v", ids, err)
	}
	want := []string{"0123456789abcde", "0123456789abcdef", "ABCDEF0123456789", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "fedcba9876543210", "notasandbox"}
	if got := entries(t, p.root); !slices.Equal(got, want) {
		t.Errorf("Root holds %v, want %v", got, want)
	}
	if _, err := readMarker(filepath.Join(outside, "x")); err != nil {
		t.Errorf("the symlink's target was touched: %v", err)
	}
}

// look is a process lookup result.
func look(present bool, start uint64, boot string) lookupResult {
	return lookupResult{present: present, start: start, boot: boot}
}

// TS-18-58: Reap's decision keeps a live owner and removes a dead owner, a
// reused pid and a marker from another boot. 18-REQ-9.2.
func TestReapDecision_TS18_58(t *testing.T) {
	m := marker{PID: 500, StartTime: 1000, BootID: "B1"}
	for _, c := range []struct {
		l    lookupResult
		keep bool
	}{
		{look(true, 1000, "B1"), true}, {look(false, 0, "B1"), false}, {look(true, 2000, "B1"), false}, {look(true, 1000, "B2"), false},
	} {
		if got := decide(m, c.l); got != c.keep {
			t.Errorf("decide(%+v) = %t, want %t", c.l, got, c.keep)
		}
	}
}

// TS-18-60: Reap skips live owners, kills a dead agent's session only on
// the same boot when its pid is gone, and removes every dead sandbox.
// 18-REQ-9.3, 18-REQ-9.4.
func TestReapKillsAndRemoves_TS18_60(t *testing.T) {
	ft := newFakeTable()
	p := reapProvider(t, ft)
	ft.add(procEntry{PID: 200, Session: 200, State: 'S', Start: 2000})
	ft.add(procEntry{PID: 201, Session: 200, State: 'S', Start: 1})
	a := "aaaaaaaaaaaaaaaa"
	sandboxWith(t, p.root, a, marker{PID: 200, StartTime: 2000, BootID: "B1", Role: "agent"})
	b := "bbbbbbbbbbbbbbbb"
	sandboxWith(t, p.root, b, marker{PID: 300, StartTime: 3000, BootID: "B1", Role: "agent"})
	ft.add(procEntry{PID: 301, Session: 300, State: 'S'})
	ft.add(procEntry{PID: 302, Session: 300, State: 'S'})
	c := "cccccccccccccccc"
	sandboxWith(t, p.root, c, marker{PID: 310, StartTime: 3100, BootID: "B0", Role: "agent"})
	ft.add(procEntry{PID: 311, Session: 310, State: 'S'})
	d := "dddddddddddddddd"
	sandboxWith(t, p.root, d, marker{PID: 320, StartTime: 3200, BootID: "B1", Role: "creator"})
	ft.add(procEntry{PID: 321, Session: 320, State: 'S'})
	e := "eeeeeeeeeeeeeeee"
	sandboxWith(t, p.root, e, marker{PID: 330, StartTime: 3300, BootID: "B1", Role: "agent"})
	ft.add(procEntry{PID: 330, Session: 330, State: 'S', Start: 9999})
	ft.add(procEntry{PID: 331, Session: 330, State: 'S'})
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{b, c, d, e}) {
		t.Fatalf("Reap = %v, %v", ids, err)
	}
	k := int(syscall.SIGKILL)
	got := ft.killed()
	slices.Sort(got)
	if want := []string{fmt.Sprintf("301/%d", k), fmt.Sprintf("302/%d", k)}; !slices.Equal(got, want) {
		t.Errorf("kills %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(p.root, a)); err != nil {
		t.Errorf("the live sandbox was removed: %v", err)
	}
	for _, x := range []string{b, c, d, e} {
		if _, err := os.Stat(filepath.Join(p.root, x)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s remains", x)
		}
	}
}

// TS-18-61: Reap returns the removed ids sorted, with the joined errors of
// failed removals. 18-REQ-9.5.
func TestReapResults_TS18_61(t *testing.T) {
	ids := []string{"ffffffffffffffff", "0000000000000000", "8888888888888888"}
	dead := marker{PID: 999, StartTime: 1, BootID: "B1", Role: "creator"}
	p := reapProvider(t, newFakeTable())
	for _, id := range ids {
		sandboxWith(t, p.root, id, dead)
	}
	got, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(got, []string{"0000000000000000", "8888888888888888", "ffffffffffffffff"}) {
		t.Errorf("Reap = %v, %v", got, err)
	}
	errInjected := errors.New("injected removal failure")
	p2 := reapProvider(t, newFakeTable(), func(s *seams) {
		s.remove = func(dir string) error {
			if strings.HasSuffix(dir, "8888888888888888") {
				return errInjected
			}
			return removeTree(dir)
		}
	})
	for _, id := range ids {
		sandboxWith(t, p2.root, id, dead)
	}
	got, err = p2.Reap(context.Background())
	if !slices.Equal(got, []string{"0000000000000000", "ffffffffffffffff"}) || !errors.Is(err, errInjected) || !strings.Contains(fmt.Sprint(err), "8888888888888888") {
		t.Errorf("with a failure: %v, %v", got, err)
	}
}
