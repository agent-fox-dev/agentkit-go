package landlock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// agentBin is agentkit-sandboxd built for the Linux tests; empty elsewhere.
var agentBin string

func TestMain(m *testing.M) {
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
		cmd := exec.Command("go", "build", "-o", agentBin, "./cmd/agentkit-sandboxd")
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building agentkit-sandboxd: %v\n%s", err, out)
			return 1
		}
		return m.Run()
	}()
	os.Exit(code)
}
