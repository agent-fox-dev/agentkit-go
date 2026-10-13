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

const makefilePath = "Makefile"

// makeDefault is the default a Makefile line `NAME ?= value` assigns.
func makeDefault(text, name string) string {
	m := regexp.MustCompile(`(?m)^` + name + `\s*\??=\s*(\S*)\s*$`).FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

// checkMakefile returns a finding, naming the Makefile and the expected
// value, for each image default that differs from the sandbox constants.
func checkMakefile(text string) []string {
	var out []string
	for name, want := range map[string]string{"SANDBOX_BASE_IMAGE": sandbox.BaseImageRepo, "SANDBOX_OVERLAY_IMAGE": sandbox.OverlayImageRepo} {
		if got := makeDefault(text, name); got != want {
			out = append(out, fmt.Sprintf("%s: %s defaults to %q, want %s", makefilePath, name, got, want))
		}
	}
	slices.Sort(out)
	return out
}

// hasToken reports whether text contains tok as a whole token: not followed
// by a character in [A-Za-z0-9_.-], since BaseImageRepo is a prefix of
// OverlayImageRepo.
func hasToken(text, tok string) bool {
	return regexp.MustCompile(regexp.QuoteMeta(tok) + `([^A-Za-z0-9_.-]|$)`).MatchString(text)
}

// checkWorkflow returns a finding, naming the workflow and the expected
// value, for each repository or Containerfile path the workflow does not
// name.
func checkWorkflow(text string) []string {
	var out []string
	for _, want := range []string{sandbox.BaseImageRepo, sandbox.OverlayImageRepo} {
		if !hasToken(text, want) {
			out = append(out, fmt.Sprintf("%s: does not name %s", workflowPath, want))
		}
	}
	if !strings.Contains(text, containerfilePath) {
		out = append(out, fmt.Sprintf("%s: does not name %s", workflowPath, containerfilePath))
	}
	return out
}

// TS-16-11: the Makefile defaults and the release workflow name the same
// repositories as the sandbox constants. 16-REQ-8.2.
func TestMakefileAndWorkflowConsistent_TS16_11(t *testing.T) {
	for _, f := range checkMakefile(readRepo(t, makefilePath)) {
		t.Error(f)
	}
	for _, f := range checkWorkflow(readRepo(t, workflowPath)) {
		t.Error(f)
	}
}

// TS-16-12: each kind of drift makes the consistency checks fail with a
// message naming the file and the expected value. 16-REQ-8.3.
func TestConsistencyDrift_TS16_12(t *testing.T) {
	cf, mk, wf, module := readRepo(t, containerfilePath), readRepo(t, makefilePath), readRepo(t, workflowPath), rootModule(t)
	if f := slices.Concat(checkContainerfile(cf, module), checkMakefile(mk), checkWorkflow(wf)); len(f) != 0 {
		t.Fatalf("the unmodified files have findings: %q", f)
	}
	expect := func(name string, findings []string, file, value string) {
		t.Helper()
		if !slices.ContainsFunc(findings, func(f string) bool { return strings.Contains(f, file) && strings.Contains(f, value) }) {
			t.Errorf("%s: findings %q name no %s and %s", name, findings, file, value)
		}
	}
	overlay := strings.Replace(cf, "FROM scratch AS overlay", "FROM debian:trixie-slim AS overlay", 1)
	expect("overlay FROM", checkContainerfile(overlay, module), containerfilePath, "FROM scratch")
	i := strings.Index(cf, "FROM debian:trixie-slim AS base")
	j := i + strings.Index(cf[i:], "\nFROM ")
	entry := cf[:i] + strings.Replace(cf[i:j], `ENTRYPOINT ["/usr/local/libexec/agentkit-sandboxd"]`, `ENTRYPOINT ["/usr/bin/agent"]`, 1) + cf[j:]
	expect("base ENTRYPOINT", checkContainerfile(entry, module), containerfilePath, sandbox.AgentPath)
	xpath := strings.ReplaceAll(cf, "-X "+module+"/sandbox/internal/buildver.stamp=", "-X example.com/other/sandbox/internal/buildver.stamp=")
	expect("-X path", checkContainerfile(xpath, module), containerfilePath, "github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=")
	mkOverlay := regexp.MustCompile(`(?m)^SANDBOX_OVERLAY_IMAGE \?= .*$`).ReplaceAllString(mk, "SANDBOX_OVERLAY_IMAGE ?= ghcr.io/other/x")
	expect("Makefile overlay", checkMakefile(mkOverlay), makefilePath, sandbox.OverlayImageRepo)
	noBase := regexp.MustCompile(regexp.QuoteMeta(sandbox.BaseImageRepo)+`([^A-Za-z0-9_.-]|$)`).ReplaceAllString(wf, "example.com/removed$1")
	if !strings.Contains(noBase, sandbox.OverlayImageRepo) {
		t.Fatal("the mutation removed the overlay repository too")
	}
	expect("workflow base", checkWorkflow(noBase), workflowPath, sandbox.BaseImageRepo)
}
