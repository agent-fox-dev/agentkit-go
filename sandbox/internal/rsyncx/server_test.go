//go:build !windows

package rsyncx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestModulesForIsWritableOnlyForSync(t *testing.T) {
	ws := t.TempDir()
	for _, d := range []struct {
		dir      Direction
		writable bool
	}{{DirSync, true}, {DirExport, false}} {
		m := ModulesFor(ws, d.dir)
		if len(m) != 1 || m[0].Root != ws || m[0].Writable != d.writable {
			t.Errorf("ModulesFor(%v) = %+v", d.dir, m)
		}
	}
}

// A transfer from a server over the filtered view to RunClient delivers
// exactly the transfer set: no excluded path, no .git, no escaping link, and
// a declared --delete deletes nothing on its own. The library does not
// confine the calling process.
func TestTransferDeliversExactlyTheFilteredTree(t *testing.T) {
	src, dest := ruleTree(t), t.TempDir()
	writeTree(t, dest, map[string]string{"stale.txt": "s"})
	rules := []string{"build", ".git", "esc", "up", "x.log", "nested/tmp"}
	fsys := Filtered(openRoot(t, src), ".", NewMatcher(rules))
	before := ServersBuilt()
	srv, err := NewServer(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if ServersBuilt() != before+1 {
		t.Errorf("ServersBuilt went from %d to %d", before, ServersBuilt())
	}
	hostArgs, err := ServerArgs(ClientArgs(true), true, ".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sEnd, cEnd := osPipe(t)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, sEnd, hostArgs); _ = sEnd.Close() }()
	if err := RunClient(ctx, cEnd, ClientArgs(true), dest); err != nil {
		t.Fatalf("RunClient: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	present, err := PresentSet(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	want := append(present, "stale.txt")
	slices.Sort(want)
	if got := listTree(t, dest); !slices.Equal(got, want) {
		t.Errorf("the destination holds %v, want the transfer set and stale.txt %v", got, want)
	}
	if l, err := os.Readlink(filepath.Join(dest, "in")); err != nil || l != "keep.txt" {
		t.Errorf("in -> %q, %v; want the link kept as a link", l, err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "after"), nil, 0o644); err != nil {
		t.Errorf("the process cannot write outside the destination after a transfer: %v", err)
	}
}

func TestExportServerRefusesAPush(t *testing.T) {
	ws, src := t.TempDir(), t.TempDir()
	writeTree(t, ws, map[string]string{"a.txt": "a"})
	writeTree(t, src, map[string]string{"pushed.txt": "p"})
	srv, err := NewServer(Filtered(openRoot(t, ws), ".", NewMatcher(nil)))
	if err != nil {
		t.Fatal(err)
	}
	args, err := ServerArgs(ClientArgs(false), false, ".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sEnd, cEnd := osPipe(t)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, sEnd, args); _ = sEnd.Close() }()
	c, err := newClient(ClientArgs(false), true)
	if err != nil {
		t.Fatal(err)
	}
	_, pushErr := c.Run(ctx, cEnd, []string{src + "/"})
	_ = cEnd.Close()
	if serveErr := <-served; pushErr == nil && serveErr == nil {
		t.Error("a push into the read-only server succeeded")
	}
	if got := listTree(t, ws); !slices.Equal(got, []string{"a.txt"}) {
		t.Errorf("the served tree now holds %v", got)
	}
}

func TestRunClientRefusesAnUnknownOption(t *testing.T) {
	_, cEnd := osPipe(t)
	if err := RunClient(context.Background(), cEnd, []string{"--bogus"}, t.TempDir()); err == nil {
		t.Error("RunClient accepted --bogus")
	}
}
