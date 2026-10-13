package landlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

// versionTimeout bounds the agent's --version run.
var versionTimeout = 10 * time.Second

const agentName = "agentkit-sandboxd"

// resolveAgent is the agent binary: agentBinary made absolute when set;
// otherwise agentkit-sandboxd beside the symlink-resolved executable, then
// the one lookPath finds. Only a regular, executable file is accepted. It
// also returns every path tried; the path is "" when none is usable.
func resolveAgent(agentBinary string, executable func() (string, error), lookPath func(string) (string, error)) (string, []string) {
	if agentBinary != "" {
		abs, err := filepath.Abs(agentBinary)
		if err != nil {
			return "", []string{agentBinary}
		}
		if isExecutable(abs) {
			return abs, []string{abs}
		}
		return "", []string{abs}
	}
	var tried []string
	if exe, err := executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		} else if d, err := filepath.EvalSymlinks(filepath.Dir(exe)); err == nil {
			exe = filepath.Join(d, filepath.Base(exe))
		}
		beside := filepath.Join(filepath.Dir(exe), agentName)
		tried = append(tried, beside)
		if isExecutable(beside) {
			return beside, tried
		}
	}
	tried = append(tried, agentName+" on PATH")
	if p, err := lookPath(agentName); err == nil {
		if abs, err := filepath.Abs(p); err == nil && isExecutable(abs) {
			return abs, tried
		}
	}
	return "", tried
}

// isExecutable reports whether p is a regular file with an execute bit (any
// regular file on Windows).
func isExecutable(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}

// installLine is the command that installs the agent for version.
func installLine(version string) string {
	if version == "(devel)" {
		return "cd sandbox && go install ./cmd/agentkit-sandboxd"
	}
	return "go install github.com/agent-fox-dev/agentkit-go/sandbox/cmd/agentkit-sandboxd@" + version
}

// runVersion runs bin --version with an empty environment, bounded by
// versionTimeout, and returns its combined output.
func runVersion(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Env = []string{}
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(out), fmt.Errorf("timed out after %v", versionTimeout)
	}
	return string(out), err
}

var versionRe = regexp.MustCompile(`^agentkit-sandboxd (\S+) \(protocol (\d+)\.(\d+)\)`)

// parseVersion is the protocol version in an agent's --version output.
func parseVersion(out string) (major, minor int, err error) {
	m := versionRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return 0, 0, errors.New("unrecognised --version output")
	}
	major, _ = strconv.Atoi(m[2])
	minor, _ = strconv.Atoi(m[3])
	return major, minor, nil
}

// checkAgent resolves the agent binary and checks that it speaks protocol
// ProtocolMajor.N with N >= 2, the first to report Landlock.
func checkAgent(o Options, s seams) (string, error) {
	install := installLine(buildver.Version())
	bin, tried := resolveAgent(o.AgentBinary, s.executable, s.lookPath)
	if bin == "" {
		return "", fmt.Errorf("landlock: no usable agentkit-sandboxd (tried %s); install it with: %s", strings.Join(tried, ", "), install)
	}
	out, err := s.runVersion(bin)
	if err == nil {
		var major, minor int
		if major, minor, err = parseVersion(out); err == nil && (major != sandbox.ProtocolMajor || minor < 2) {
			err = fmt.Errorf("it speaks protocol %d.%d; this provider needs %d.2 or later", major, minor, sandbox.ProtocolMajor)
		}
	}
	if err != nil {
		return "", fmt.Errorf("landlock: %s --version: %v: output %q; install a matching agent with: %s", bin, err, strings.TrimSpace(out), install)
	}
	return bin, nil
}
