package landlock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// agentBin and dialtcpBin are agentkit-sandboxd and the dialtcp helper
// built for the Linux tests; empty elsewhere.
var agentBin, dialtcpBin string

// childVar selects a re-executed child; childAgentVar carries agentBin to it.
const (
	childVar      = "AGENTKIT_LANDLOCK_CHILD"
	childAgentVar = "AGENTKIT_LANDLOCK_AGENT"
)

func TestMain(m *testing.M) {
	if name := os.Getenv(childVar); name != "" {
		agentBin = os.Getenv(childAgentVar)
		os.Exit(runChild(name))
	}
	code := func() int {
		if runtime.GOOS != "linux" {
			return m.Run()
		}
		dir, err := os.MkdirTemp("", "landlock-test-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(dir)
		agentBin = filepath.Join(dir, "agentkit-sandboxd")
		dialtcpBin = filepath.Join(dir, "dialtcp", "dialtcp")
		for _, b := range [][2]string{{agentBin, "./cmd/agentkit-sandboxd"}, {dialtcpBin, "./cmd/agentkit-sandboxd/testdata/dialtcp"}} {
			cmd := exec.Command("go", "build", "-o", b[0], b[1])
			cmd.Dir = ".."
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if out, err := cmd.CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "building %s: %v\n%s", b[1], err, out)
				return 1
			}
		}
		return m.Run()
	}()
	os.Exit(code)
}
