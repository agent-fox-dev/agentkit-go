package landlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// sandboxWith writes a sandbox directory id under root whose marker is m.
func sandboxWith(t *testing.T, root, id string, m marker) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "workspace"), 0o700); err != nil {
		t.Fatal(err)
	}
	m.Created = time.Now()
	if err := writeMarker(dir, m); err != nil {
		t.Fatal(err)
	}
}

// Reap considers only directories (not symlinks) named by 16 lowercase hex
// characters with a readable marker, and leaves everything else alone.
func TestReapConsidersOnlySandboxDirectories(t *testing.T) {
	p := newTestProvider(t, Options{}, func(s *seams) { s.procs = newFakeTable() })
	dead := marker{PID: 999, StartTime: 1, BootID: "B1", Role: "agent"}
	outside := t.TempDir()
	sandboxWith(t, outside, "x", dead)
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(p.root, "fedcba9876543210")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ABCDEF0123456789", "0123456789abcde", "notasandbox", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"} {
		if err := os.Mkdir(filepath.Join(p.root, n), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"0123456789abcdef": "file", "bbbbbbbbbbbbbbbb/" + markerName: "{garbage"} {
		if err := os.WriteFile(filepath.Join(p.root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sandboxWith(t, p.root, "cccccccccccccccc", dead)
	if ids, err := p.Reap(context.Background()); err != nil || !slices.Equal(ids, []string{"cccccccccccccccc"}) {
		t.Fatalf("Reap = %v, %v", ids, err)
	}
	want := []string{"0123456789abcde", "0123456789abcdef", "ABCDEF0123456789", "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb", "fedcba9876543210", "notasandbox"}
	if got := entries(t, p.root); !slices.Equal(got, want) || len(entries(t, filepath.Join(outside, "x"))) != 2 {
		t.Errorf("Root holds %v, want %v; the symlink's target holds %v", got, want, entries(t, filepath.Join(outside, "x")))
	}
}

// A sandbox stays exactly when its owner has the recorded pid, start time and
// boot; a dead agent's session is killed first.
func TestReapRemovesSandboxesWhoseOwnerIsGone(t *testing.T) {
	ft := newFakeTable()
	p := newTestProvider(t, Options{}, func(s *seams) { s.procs = ft })
	ft.add(procEntry{PID: 200, Session: 200, State: 'S', Start: 2000}, procEntry{PID: 201, Session: 200, State: 'S'},
		procEntry{PID: 301, Session: 300, State: 'S'}, procEntry{PID: 302, Session: 300, State: 'S'},
		procEntry{PID: 311, Session: 310, State: 'S'}, procEntry{PID: 321, Session: 320, State: 'S'},
		procEntry{PID: 330, Session: 330, State: 'S', Start: 9999}, procEntry{PID: 331, Session: 330, State: 'S'},
		procEntry{PID: 340, Session: 340, State: 'S', Start: 3400})
	for id, m := range map[string]marker{
		"aaaaaaaaaaaaaaaa": {PID: 200, StartTime: 2000, BootID: "B1", Role: "agent"},   // alive
		"bbbbbbbbbbbbbbbb": {PID: 300, StartTime: 3000, BootID: "B1", Role: "agent"},   // gone: its session is killed
		"cccccccccccccccc": {PID: 310, StartTime: 3100, BootID: "B0", Role: "agent"},   // another boot
		"dddddddddddddddd": {PID: 320, StartTime: 3200, BootID: "B1", Role: "creator"}, // a gone creator
		"eeeeeeeeeeeeeeee": {PID: 330, StartTime: 3300, BootID: "B1", Role: "agent"},   // a reused pid
		"ffffffffffffffff": {PID: 340, StartTime: 3400, BootID: "B1", Role: "creator"}, // alive
	} {
		sandboxWith(t, p.root, id, m)
	}
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{"bbbbbbbbbbbbbbbb", "cccccccccccccccc", "dddddddddddddddd", "eeeeeeeeeeeeeeee"}) {
		t.Fatalf("Reap = %v, %v", ids, err)
	}
	if slices.Sort(ft.kills); !slices.Equal(ft.kills, []int{301, 302}) || !slices.Equal(entries(t, p.root), []string{"aaaaaaaaaaaaaaaa", "ffffffffffffffff"}) {
		t.Errorf("killed %v; Root holds %v", ft.kills, entries(t, p.root))
	}
}

// Reap returns the removed ids sorted, and the joined errors of failed
// removals naming their sandboxes; a missing Root reaps nothing.
func TestReapReportsWhatItRemovedAndWhatFailed(t *testing.T) {
	errRemove := errors.New("injected removal failure")
	p := newTestProvider(t, Options{}, func(s *seams) {
		s.procs = newFakeTable()
		s.remove = func(dir string) error {
			if strings.HasSuffix(dir, "8888888888888888") {
				return errRemove
			}
			return removeTree(dir)
		}
	})
	for _, id := range []string{"ffffffffffffffff", "0000000000000000", "8888888888888888"} {
		sandboxWith(t, p.root, id, marker{PID: 999, StartTime: 1, BootID: "B1", Role: "creator"})
	}
	ids, err := p.Reap(context.Background())
	if !slices.Equal(ids, []string{"0000000000000000", "ffffffffffffffff"}) || !errors.Is(err, errRemove) || !strings.Contains(err.Error(), "reaping 8888888888888888") {
		t.Errorf("Reap = %v, %v", ids, err)
	}
	if err := os.RemoveAll(p.root); err != nil {
		t.Fatal(err)
	}
	if ids, err := p.Reap(context.Background()); ids != nil || err != nil {
		t.Errorf("Reap of a missing Root = %v, %v", ids, err)
	}
}
