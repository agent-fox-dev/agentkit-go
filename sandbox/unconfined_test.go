//go:build unix

package sandbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

const (
	unconfinedMarker = "AGENTKIT_SANDBOX_UNCONFINED_CHILD"
	unconfinedDone   = "UNCONFINED-CHILD-COMPLETE"
)

// sysFiles are regular files under /sys, one of which a Linux host has.
var sysFiles = []string{"/sys/devices/system/cpu/online", "/sys/kernel/mm/transparent_hugepage/enabled",
	"/sys/power/state", "/sys/kernel/uevent_seqnum"}

// TS-14-49: a re-executed test child runs a LocalDir Sync and Export over
// local and remains unconfined, with its working directory, uid and umask
// unchanged. 14-REQ-7.1, 14-PATH-4.
func TestUnconfined_TS14_49(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnconfinedChild_TS14_49$", "-test.count=1", "-test.v")
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, unconfinedMarker+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, unconfinedMarker+"=1", "PATH=", "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), unconfinedDone) {
		t.Fatalf("the child: %v\n%s", err, out)
	}
}

// TestUnconfinedChild_TS14_49 is TS-14-49's child role; without the marker
// it does nothing.
func TestUnconfinedChild_TS14_49(t *testing.T) {
	if os.Getenv(unconfinedMarker) != "1" {
		t.Skip("runs only as TS-14-49's re-executed child")
	}
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		fmt.Fprintln(os.Stderr, "unconfined child: "+msg)
		t.Error(msg)
	}
	wd0, _ := os.Getwd()
	uid0, mask0 := os.Getuid(), currentUmask()
	ctx := context.Background()
	sb, err := local.Unisolated().Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close(ctx)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		fail("Sync: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst}); err != nil {
		fail("Export: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "a" {
		fail("the export did not deliver a.txt: %q, %v", b, err)
	}

	dir, err := os.MkdirTemp("", "unconfined-")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "probe"), []byte("p"), 0o644)
		_ = os.RemoveAll(dir)
	}
	if err != nil {
		fail("creating a file outside the workspace: %v", err)
	}
	if runtime.GOOS == "linux" {
		var errs []error
		read := false
		for _, f := range sysFiles {
			if _, err := os.ReadFile(f); err == nil {
				read = true
				break
			} else {
				errs = append(errs, err)
			}
		}
		if !read {
			fail("reading under /sys: %v", errors.Join(errs...))
		}
	}
	if wd, _ := os.Getwd(); wd != wd0 {
		fail("the working directory moved from %s to %s", wd0, wd)
	}
	if uid := os.Getuid(); uid != uid0 {
		fail("the uid changed from %d to %d", uid0, uid)
	}
	if m := currentUmask(); m != mask0 {
		fail("the umask changed from %#o to %#o", mask0, m)
	}
	if !t.Failed() {
		fmt.Println(unconfinedDone)
	}
}
