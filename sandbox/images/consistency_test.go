package images

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

const containerfilePath = "sandbox/images/Containerfile"

// checkContainerfile returns a finding, naming the file and the expected
// value, for each way text breaks the stage layout the images rely on.
// module is the root module's path.
func checkContainerfile(text, module string) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, containerfilePath+": "+fmt.Sprintf(format, args...))
	}
	stages := parseContainerfile(text)
	base, overlay := find(stages, "base"), find(stages, "overlay")
	if base == nil {
		add("no stage named base")
	}
	if overlay == nil {
		add("no stage named overlay")
	} else if f := strings.Fields(overlay.from); len(f) == 0 || f[0] != "scratch" {
		add("the overlay stage starts FROM %s, want FROM scratch", overlay.from)
	}
	for _, c := range []struct {
		s   *stage
		cgo string
	}{{base, "1"}, {overlay, "0"}} {
		if c.s == nil {
			continue
		}
		build := buildStage(stages, c.cgo)
		copies := 0
		for _, a := range c.s.all("COPY") {
			if cp := parseCopy(a); cp.from != "" && cp.dest == sandbox.AgentPath {
				copies++
				if build == nil || cp.from != build.name {
					add("the %s stage copies the agent from %q, want the CGO_ENABLED=%s stage", c.s.name, cp.from, c.cgo)
				}
			}
		}
		if copies == 0 {
			add("the %s stage has no COPY --from=<build stage> to sandbox.AgentPath %s", c.s.name, sandbox.AgentPath)
		}
		if ep := c.s.entrypoint(); !slices.Equal(ep, []string{sandbox.AgentPath}) {
			add("the %s stage's ENTRYPOINT is %v, want [sandbox.AgentPath] [%s]", c.s.name, ep, sandbox.AgentPath)
		}
	}
	if want := module + "/sandbox/internal/buildver.stamp="; !strings.Contains(text, "-X "+want) {
		add("no -X %s", want)
	}
	return out
}

// rootModule is the module path go.mod declares.
func rootModule(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^module (\S+)$`).FindStringSubmatch(readRepo(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod declares no module")
	}
	return m[1]
}

// TS-16-10: the Containerfile has base and overlay stages, FROM scratch for
// the overlay, AgentPath in COPY and ENTRYPOINT, and the stamp -X path.
// 16-REQ-8.1, 16-REQ-1.1.
func TestContainerfileConsistent_TS16_10(t *testing.T) {
	for _, f := range checkContainerfile(readRepo(t, containerfilePath), rootModule(t)) {
		t.Error(f)
	}
}
