//go:build linux

package confine

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

const (
	childMarker  = "AGENTKIT_CONFINE_CHILD"
	childArgsVar = "AGENTKIT_CONFINE_ARGS"
	childDone    = "CONFINE-CHILD-COMPLETE"
)

// kernelABI probes Landlock independently of the package:
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION), number
// 444 on amd64 and arm64.
func kernelABI() (int, error) {
	r, _, errno := syscall.Syscall(444, 0, 0, 1)
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}

// needABI skips the test, naming what it found, unless the kernel offers at
// least Landlock ABI min.
func needABI(t *testing.T, min int) int {
	t.Helper()
	k, err := kernelABI()
	if err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	if k < min {
		t.Skipf("kernel Landlock ABI %d, the test needs %d", k, min)
	}
	return k
}

// childArgs are what a parent hands a child: paths and an address.
type childArgs struct {
	Dirs map[string]string
	Addr string
	Sim  int
	Mode int
}

// runChild re-executes the test binary to run the test named name as a
// child with args, so that a ruleset confines only the child, and requires
// exit status 0 and the completion marker, so a skipped child fails.
func runChild(t *testing.T, name string, args childArgs) string {
	t.Helper()
	b, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), childMarker+"="+name, childArgsVar+"="+string(b))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), childDone) {
		t.Fatalf("child %s: %v\n%s", name, err, out)
	}
	return string(out)
}

// child reports whether this process runs t as a child, returning its args;
// on success the child prints the completion marker. A child must create no
// t.TempDir: its cleanup could not remove it once confined.
func child(t *testing.T) (childArgs, bool) {
	t.Helper()
	if os.Getenv(childMarker) != t.Name() {
		return childArgs{}, false
	}
	var a childArgs
	if err := json.Unmarshal([]byte(os.Getenv(childArgsVar)), &a); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			fmt.Println(childDone)
		}
	})
	return a, true
}

// noNewPrivs is prctl(PR_GET_NO_NEW_PRIVS).
func noNewPrivs() int {
	r, _, _ := syscall.Syscall(syscall.SYS_PRCTL, 39, 0, 0)
	return int(r)
}
