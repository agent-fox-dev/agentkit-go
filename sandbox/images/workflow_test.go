package images

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const workflowPath = ".github/workflows/sandbox-images.yml"

// ---- a reader for the subset of YAML the workflow uses ----

// yparser reads indentation-based mappings and sequences, `|` literal
// blocks, flow sequences and quoted scalars, with no YAML dependency.
type yparser struct {
	lines []string
	i     int
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

func (p *yparser) skip() {
	for p.i < len(p.lines) {
		t := strings.TrimSpace(p.lines[p.i])
		if t != "" && !strings.HasPrefix(t, "#") {
			return
		}
		p.i++
	}
}

func (p *yparser) block(min int) any {
	p.skip()
	if p.i >= len(p.lines) || indentOf(p.lines[p.i]) < min {
		return nil
	}
	ind := indentOf(p.lines[p.i])
	if t := strings.TrimSpace(p.lines[p.i]); t == "-" || strings.HasPrefix(t, "- ") {
		return p.seq(ind)
	}
	return p.mapping(ind)
}

// splitKey splits `key: value` (or `key:`) at the first colon followed by a
// space or the end of the line.
func splitKey(t string) (string, string, bool) {
	if strings.HasPrefix(t, `"`) || strings.HasPrefix(t, `'`) {
		return "", "", false
	}
	if i := strings.Index(t, ": "); i > 0 {
		return t[:i], strings.TrimSpace(t[i+2:]), true
	}
	if strings.HasSuffix(t, ":") {
		return strings.TrimSuffix(t, ":"), "", true
	}
	return "", "", false
}

func (p *yparser) mapping(ind int) map[string]any {
	m := map[string]any{}
	for {
		p.skip()
		if p.i >= len(p.lines) || indentOf(p.lines[p.i]) != ind {
			return m
		}
		t := strings.TrimSpace(p.lines[p.i])
		k, v, ok := splitKey(t)
		if !ok || strings.HasPrefix(t, "- ") {
			return m
		}
		p.i++
		m[k] = p.value(v, ind)
	}
}

func (p *yparser) seq(ind int) []any {
	var s []any
	for {
		p.skip()
		if p.i >= len(p.lines) || indentOf(p.lines[p.i]) != ind {
			return s
		}
		t := strings.TrimSpace(p.lines[p.i])
		if t != "-" && !strings.HasPrefix(t, "- ") {
			return s
		}
		item := strings.TrimSpace(strings.TrimPrefix(t, "-"))
		p.i++
		if k, v, ok := splitKey(item); ok {
			m := map[string]any{k: p.value(v, ind+2)}
			for k2, v2 := range p.mapping(ind + 2) {
				m[k2] = v2
			}
			s = append(s, m)
		} else {
			s = append(s, unquote(item))
		}
	}
}

func (p *yparser) value(v string, ind int) any {
	switch {
	case v == "|" || v == "|-":
		return p.literal(ind)
	case v == "":
		if b := p.block(ind + 1); b != nil {
			return b
		}
		return nil
	case strings.HasPrefix(v, "["):
		var out []any
		for _, f := range strings.Split(strings.Trim(v, "[]"), ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, unquote(f))
			}
		}
		return out
	}
	return unquote(v)
}

func (p *yparser) literal(ind int) string {
	var body []string
	base := -1
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if strings.TrimSpace(l) != "" && indentOf(l) <= ind {
			break
		}
		if base < 0 && strings.TrimSpace(l) != "" {
			base = indentOf(l)
		}
		if len(l) >= base && base >= 0 {
			l = l[base:]
		} else {
			l = strings.TrimSpace(l)
		}
		body = append(body, l)
		p.i++
	}
	return strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// parseWorkflow reads text into nested maps, slices and strings.
func parseWorkflow(text string) map[string]any {
	p := &yparser{lines: strings.Split(text, "\n")}
	m, _ := p.block(0).(map[string]any)
	return m
}

// ---- navigation ----

func mget(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) string { s, _ := v.(string); return s }

type wfStep = map[string]any

func steps(t *testing.T, wf map[string]any, job string) []wfStep {
	t.Helper()
	raw, ok := mget(wf, "jobs", job, "steps").([]any)
	if !ok {
		t.Fatalf("job %s has no steps", job)
	}
	var out []wfStep
	for _, s := range raw {
		m, _ := s.(map[string]any)
		out = append(out, m)
	}
	return out
}

func stepIndex(ss []wfStep, match func(wfStep) bool) int {
	for i, s := range ss {
		if match(s) {
			return i
		}
	}
	return -1
}

func usesAction(name string) func(wfStep) bool {
	return func(s wfStep) bool { return strings.HasPrefix(str(s["uses"]), name+"@") }
}

func runContains(sub string) func(wfStep) bool {
	return func(s wfStep) bool { return strings.Contains(str(s["run"]), sub) }
}

func readWorkflow(t *testing.T) (string, map[string]any) {
	t.Helper()
	text := readRepo(t, workflowPath)
	return text, parseWorkflow(text)
}

// ---- running run blocks ----

// fakeDocker is a docker on PATH that logs each argv to $FAKE_LOG; pull
// fails when its argv equals $FAKE_PULL_FAIL, and run prints $FAKE_RUN_OUT.
const fakeDocker = `#!/bin/sh
echo "$*" >> "$FAKE_LOG"
case "$1" in
  pull) if [ -n "$FAKE_PULL_FAIL" ] && [ "$*" = "$FAKE_PULL_FAIL" ]; then echo denied >&2; exit 1; fi ;;
  run) printf '%s\n' "${FAKE_RUN_OUT:-agentkit-sandboxd v1.2.3 (protocol 1.0)}" ;;
esac
exit 0
`

// runBash runs block as Actions does (bash --noprofile --norc -eo pipefail)
// with env and a fake docker first on PATH; it returns the combined
// output, the docker log and the exit code.
func runBash(t *testing.T, block string, env ...string) (string, []string, int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not on PATH")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "step.sh")
	if err := os.WriteFile(script, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "docker.log")
	cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", script)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + dir + ":" + os.Getenv("PATH"), "FAKE_LOG=" + log}, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var calls []string
	if b, err := os.ReadFile(log); err == nil {
		calls = strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	return out.String(), calls, code
}

// versionStep runs the images job's version step and returns its output,
// its exit code and the version and tags lines it wrote.
func versionStep(t *testing.T, wf map[string]any, env ...string) (string, int, string, string) {
	t.Helper()
	ss := steps(t, wf, "images")
	i := stepIndex(ss, func(s wfStep) bool { return str(s["id"]) == "version" })
	if i < 0 {
		t.Fatal("the images job has no step with id version")
	}
	block := str(ss[i]["run"])
	if strings.Contains(block, "${{") {
		t.Fatal("the version step interpolates ${{ }}; it must read only the environment")
	}
	outFile := filepath.Join(t.TempDir(), "output")
	out, _, code := runBash(t, block, append(env, "GITHUB_OUTPUT="+outFile)...)
	b, _ := os.ReadFile(outFile)
	version, tags := "", ""
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "version="); ok {
			version = v
		}
		if v, ok := strings.CutPrefix(l, "tags="); ok {
			tags = v
		}
	}
	return out, code, version, tags
}

func tagPush(ref string) []string {
	return []string{"GITHUB_EVENT_NAME=push", "GITHUB_REF_TYPE=tag", "GITHUB_REF=refs/tags/" + ref, "GITHUB_REF_NAME=" + ref}
}

// tagPushIf reports whether an if: condition requires a tag push.
func tagPushIf(cond string) bool {
	return strings.Contains(cond, "github.event_name == 'push'") &&
		(strings.Contains(cond, "startsWith(github.ref, 'refs/tags/')") || strings.Contains(cond, "github.ref_type == 'tag'"))
}

// ---- tests ----

// TS-16-30: the release workflow triggers on v* tags and dispatch, grants
// least privilege, serialises per ref, and uses only the five pinned
// actions. 16-REQ-9.1, 16-REQ-9.8.
func TestWorkflowShape_TS16_30(t *testing.T) {
	text, wf := readWorkflow(t)
	tags, _ := mget(wf, "on", "push", "tags").([]any)
	on, _ := wf["on"].(map[string]any)
	if !slices.Contains(tags, any("v*")) || on == nil {
		t.Errorf("on: %v", wf["on"])
	} else if _, ok := on["workflow_dispatch"]; !ok {
		t.Error("no workflow_dispatch trigger")
	}
	if p, _ := wf["permissions"].(map[string]any); len(p) != 1 || p["contents"] != "read" {
		t.Errorf("top-level permissions %v", wf["permissions"])
	}
	if n := strings.Count(text, "packages: write"); n != 1 || mget(wf, "jobs", "images", "permissions", "packages") != "write" {
		t.Errorf("packages: write occurs %d times, images job %v", n, mget(wf, "jobs", "images", "permissions"))
	}
	if mget(wf, "jobs", "verify", "permissions", "packages") != nil {
		t.Error("the verify job asks for packages permission")
	}
	if g := str(mget(wf, "concurrency", "group")); !strings.Contains(g, "github.ref") || str(mget(wf, "concurrency", "cancel-in-progress")) != "false" {
		t.Errorf("concurrency %v", wf["concurrency"])
	}
	usesRe := regexp.MustCompile(`^(actions/checkout|actions/setup-go|docker/setup-qemu-action|docker/setup-buildx-action|docker/login-action)@v[0-9]+$`)
	for _, m := range regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)`).FindAllStringSubmatch(text, -1) {
		if !usesRe.MatchString(m[1]) {
			t.Errorf("uses: %s", m[1])
		}
	}
	if strings.Contains(text, "docker/metadata-action") {
		t.Error("the workflow uses docker/metadata-action")
	}
}

// TS-16-31: the version step rejects a tag that is not a canonical version,
// naming the ref, before anything is built or pushed. 16-REQ-9.2.
func TestVersionStepRejects_TS16_31(t *testing.T) {
	text, wf := readWorkflow(t)
	for _, ref := range []string{"v1.2", "1.2.3", "v01.2.3", "v1.02.3", "v1.2.3+build", "v1.2.3-", "v1.2.3-rc_1", "main"} {
		out, code, _, tags := versionStep(t, wf, tagPush(ref)...)
		if code == 0 || !strings.Contains(out, ref) || tags != "" {
			t.Errorf("ref %q: exit %d, tags %q, output %q", ref, code, tags, out)
		}
	}
	ss := steps(t, wf, "images")
	v := stepIndex(ss, func(s wfStep) bool { return str(s["id"]) == "version" })
	for i, s := range ss {
		if (runContains("build")(s) || runContains("--push")(s) || usesAction("docker/login-action")(s)) && i < v {
			t.Errorf("step %d (%v) runs before the version step", i, s["name"])
		}
	}
	if strings.Contains(text, "continue-on-error") {
		t.Error("a step has continue-on-error")
	}
}

// TS-16-32: for any canonical version tag, the version step outputs the ref
// name and the tags X.Y.Z plus X.Y only without a prerelease. 16-REQ-9.3.
func TestVersionStepTags_TS16_32(t *testing.T) {
	_, wf := readWorkflow(t)
	r := rand.New(rand.NewPCG(16, 32))
	part := func() string {
		if r.IntN(3) == 0 {
			return "0"
		}
		s := fmt.Sprint(1 + r.IntN(9))
		for range r.IntN(5) {
			s += fmt.Sprint(r.IntN(10))
		}
		return s
	}
	const preChars = "0123456789ABCabcxyz.-"
	refs := []string{"v1.2.3", "v1.2.3-rc.1", "v0.6.0"}
	for range 300 {
		ref := "v" + part() + "." + part() + "." + part()
		if r.IntN(2) == 0 {
			n := 1 + r.IntN(12)
			b := make([]byte, n)
			for i := range b {
				b[i] = preChars[r.IntN(len(preChars))]
			}
			ref += "-" + string(b)
		}
		refs = append(refs, ref)
	}
	for _, ref := range refs {
		out, code, version, tags := versionStep(t, wf, tagPush(ref)...)
		core, _, hasPre := strings.Cut(strings.TrimPrefix(ref, "v"), "-")
		want := []string{strings.TrimPrefix(ref, "v")}
		if !hasPre {
			f := strings.Split(core, ".")
			want = append(want, f[0]+"."+f[1])
		}
		got := strings.Fields(tags)
		slices.Sort(got)
		slices.Sort(want)
		if code != 0 || version != ref || !slices.Equal(got, want) {
			t.Fatalf("ref %q: exit %d, version %q, tags %q, want %v (output %q)", ref, code, version, tags, want, out)
		}
	}
}

// TS-16-33: the images job checks out, sets up Go from go.mod, and runs the
// image tests on the default builder before any QEMU, buildx, login or push
// step, which a test failure skips. 16-REQ-9.4, 16-REQ-9.5.
func TestImagesJobOrder_TS16_33(t *testing.T) {
	text, wf := readWorkflow(t)
	if str(mget(wf, "jobs", "images", "runs-on")) != "ubuntu-24.04" {
		t.Errorf("runs-on %v", mget(wf, "jobs", "images", "runs-on"))
	}
	ss := steps(t, wf, "images")
	co, sg := stepIndex(ss, usesAction("actions/checkout")), stepIndex(ss, usesAction("actions/setup-go"))
	if co < 0 || sg < co || str(mget(ss[sg], "with", "go-version-file")) != "go.mod" {
		t.Errorf("checkout at %d, setup-go at %d with %v", co, sg, ss[max(sg, 0)]["with"])
	}
	test := stepIndex(ss, runContains("make sandbox-images-test"))
	if test < 0 {
		t.Fatal("no step runs make sandbox-images-test")
	}
	run := str(ss[test]["run"])
	for _, want := range []string{"CONTAINER=docker", "SANDBOX_IMAGE_TAG=ci", `AGENTKIT_VERSION="$VERSION"`, `AGENTKIT_REVISION="$GITHUB_SHA"`} {
		if !strings.Contains(run, want) {
			t.Errorf("the test step %q lacks %s", run, want)
		}
	}
	if !strings.Contains(str(mget(ss[test], "env", "VERSION")), "steps.version.outputs.version") || ss[test]["if"] != nil {
		t.Errorf("the test step's env %v, if %v", ss[test]["env"], ss[test]["if"])
	}
	for i, s := range ss {
		late := usesAction("docker/setup-qemu-action")(s) || usesAction("docker/setup-buildx-action")(s) || usesAction("docker/login-action")(s) || runContains("--push")(s)
		if late && i < test {
			t.Errorf("step %d (%v) runs before the test step", i, s["name"])
		}
		if late {
			for _, bad := range []string{"always()", "failure()", "cancelled()"} {
				if strings.Contains(str(s["if"]), bad) {
					t.Errorf("step %d's if: %q runs after a failure", i, s["if"])
				}
			}
		}
	}
	if strings.Contains(text, "continue-on-error") {
		t.Error("a step has continue-on-error")
	}
}

// TS-16-34: the publish step pushes both targets for linux/amd64 and
// linux/arm64 with every computed tag on its own repository after logging
// in to GHCR. 16-REQ-9.6.
func TestPublishStep_TS16_34(t *testing.T) {
	_, wf := readWorkflow(t)
	ss := steps(t, wf, "images")
	q, b, l, p := stepIndex(ss, usesAction("docker/setup-qemu-action")), stepIndex(ss, usesAction("docker/setup-buildx-action")),
		stepIndex(ss, usesAction("docker/login-action")), stepIndex(ss, runContains("--push"))
	if q < 0 || b < 0 || l < q || l < b || p < l {
		t.Fatalf("qemu %d, buildx %d, login %d, push %d", q, b, l, p)
	}
	with := ss[l]["with"]
	if str(mget(with, "registry")) != "ghcr.io" || !strings.Contains(str(mget(with, "username")), "github.actor") ||
		str(mget(with, "password")) != "${{ secrets.GITHUB_TOKEN }}" {
		t.Errorf("login with %v", with)
	}
	block := str(ss[p]["run"])
	if strings.Contains(block, "${{") {
		t.Fatal("the push step interpolates ${{ }}")
	}
	env, _ := ss[p]["env"].(map[string]any)
	if !strings.Contains(str(env["VERSION"]), "steps.version.outputs.version") || !strings.Contains(str(env["TAGS"]), "steps.version.outputs.tags") {
		t.Errorf("push env %v", env)
	}
	out, calls, code := runBash(t, block, "VERSION=v1.2.3", "TAGS=1.2.3 1.2", "GITHUB_SHA=abc")
	if code != 0 || len(calls) != 2 {
		t.Fatalf("exit %d, docker calls %q\n%s", code, calls, out)
	}
	for _, c := range calls {
		for _, want := range []string{"buildx build", "--platform linux/amd64,linux/arm64", "-f sandbox/images/Containerfile", "--push",
			"--build-arg AGENTKIT_VERSION=v1.2.3", "--build-arg AGENTKIT_REVISION=abc"} {
			if !strings.Contains(c, want) {
				t.Errorf("%q lacks %q", c, want)
			}
		}
	}
	tagsOf := func(c string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`-t (\S+)`).FindAllStringSubmatch(c, -1) {
			out = append(out, m[1])
		}
		slices.Sort(out)
		return out
	}
	for _, c := range calls {
		switch {
		case strings.Contains(c, "--target base"):
			if got := tagsOf(c); !slices.Equal(got, []string{"quay.io/agent-fox-dev/agentkit-sandbox:1.2", "quay.io/agent-fox-dev/agentkit-sandbox:1.2.3"}) {
				t.Errorf("base tags %v", got)
			}
		case strings.Contains(c, "--target overlay"):
			if got := tagsOf(c); !slices.Equal(got, []string{"quay.io/agent-fox-dev/agentkit-sandboxd:1.2", "quay.io/agent-fox-dev/agentkit-sandboxd:1.2.3"}) {
				t.Errorf("overlay tags %v", got)
			}
		default:
			t.Errorf("a push without a known target: %q", c)
		}
	}
}

// TS-16-35: a dispatch run tests with an empty version, computes no tags,
// and every QEMU, login and push step and the verify job are gated on a tag
// push. 16-REQ-9.7.
func TestDispatchDryRun_TS16_35(t *testing.T) {
	_, wf := readWorkflow(t)
	for _, env := range [][]string{
		{"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_REF_TYPE=branch", "GITHUB_REF_NAME=main"},
		{"GITHUB_EVENT_NAME=workflow_dispatch", "GITHUB_REF_TYPE=tag", "GITHUB_REF_NAME=v1.2.3"},
	} {
		out, code, version, tags := versionStep(t, wf, env...)
		if code != 0 || version != "" || tags != "" {
			t.Errorf("%v: exit %d, version %q, tags %q (%s)", env, code, version, tags, out)
		}
	}
	ss := steps(t, wf, "images")
	for i, s := range ss {
		gated := usesAction("docker/setup-qemu-action")(s) || usesAction("docker/setup-buildx-action")(s) || usesAction("docker/login-action")(s) || runContains("--push")(s)
		if gated && !tagPushIf(str(s["if"])) {
			t.Errorf("step %d (%v) has if: %q, not gated on a tag push", i, s["name"], s["if"])
		}
	}
	if !tagPushIf(str(mget(wf, "jobs", "verify", "if"))) {
		t.Errorf("verify's if: %q", mget(wf, "jobs", "verify", "if"))
	}
	if test := stepIndex(ss, runContains("make sandbox-images-test")); test < 0 || ss[test]["if"] != nil {
		t.Error("the test step is gated, so a dispatch would not test")
	}
}

// verifyBlock is the verify job's run block, checked to read its inputs
// only from needs.images.outputs.
func verifyBlock(t *testing.T, wf map[string]any) string {
	t.Helper()
	ss := steps(t, wf, "verify")
	i := stepIndex(ss, runContains("--version"))
	if i < 0 {
		t.Fatal("the verify job runs no --version")
	}
	env, _ := ss[i]["env"].(map[string]any)
	if !strings.Contains(str(env["VERSION"]), "needs.images.outputs.version") || !strings.Contains(str(env["TAGS"]), "needs.images.outputs.tags") {
		t.Errorf("verify env %v", env)
	}
	block := str(ss[i]["run"])
	if strings.Contains(block, "${{") {
		t.Fatal("the verify step interpolates ${{ }}")
	}
	return block
}

// TS-16-36: the verify job, after images and without logging in, pulls
// every repository, tag and platform and checks each agent's --version
// prefix. 16-REQ-10.1, 16-REQ-10.2.
func TestVerifyJob_TS16_36(t *testing.T) {
	text, wf := readWorkflow(t)
	if str(mget(wf, "jobs", "verify", "needs")) != "images" {
		t.Errorf("verify needs %v", mget(wf, "jobs", "verify", "needs"))
	}
	ss := steps(t, wf, "verify")
	if stepIndex(ss, usesAction("docker/setup-qemu-action")) < 0 || stepIndex(ss, usesAction("docker/login-action")) >= 0 {
		t.Error("verify must set up QEMU and must not log in")
	}
	if strings.Contains(text, "docker login") {
		t.Error("a run block logs in")
	}
	out, calls, code := runBash(t, verifyBlock(t, wf), "VERSION=v1.2.3", "TAGS=1.2.3 1.2")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, repo := range []string{"quay.io/agent-fox-dev/agentkit-sandbox", "quay.io/agent-fox-dev/agentkit-sandboxd"} {
		for _, tag := range []string{"1.2.3", "1.2"} {
			for _, p := range []string{"linux/amd64", "linux/arm64"} {
				ref := repo + ":" + tag
				if !slices.Contains(calls, "pull --platform "+p+" "+ref) {
					t.Errorf("no pull of %s for %s", ref, p)
				}
				if !slices.ContainsFunc(calls, func(c string) bool {
					f := strings.Fields(c)
					return len(f) > 0 && f[0] == "run" && strings.Contains(c, "--rm") && strings.Contains(c, "--platform "+p) &&
						strings.Contains(c, "--entrypoint /usr/local/libexec/agentkit-sandboxd") && slices.Contains(f, ref) && f[len(f)-1] == "--version"
				}) {
					t.Errorf("no --version run of %s for %s", ref, p)
				}
			}
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(calls), func(c string) bool { return !strings.HasPrefix(c, "pull ") })); n != 8 {
		t.Errorf("%d pulls, want 8", n)
	}
}

// TS-16-37: the verify job fails naming the reference when an anonymous
// pull fails or the version prefix is wrong, and touches no published
// image. 16-REQ-10.3.
func TestVerifyJobFails_TS16_37(t *testing.T) {
	_, wf := readWorkflow(t)
	block := verifyBlock(t, wf)
	for _, c := range []struct {
		env  []string
		name string
	}{
		{[]string{"FAKE_PULL_FAIL=pull --platform linux/arm64 quay.io/agent-fox-dev/agentkit-sandboxd:1.2"}, "quay.io/agent-fox-dev/agentkit-sandboxd:1.2"},
		{[]string{"FAKE_RUN_OUT=agentkit-sandboxd v1.2.2 (protocol 1.0)"}, "quay.io/agent-fox-dev/agentkit-sandbox:1.2.3"},
		{[]string{"FAKE_RUN_OUT=agentkit-sandboxd v1.2.30 (protocol 1.0)"}, "quay.io/agent-fox-dev/agentkit-sandbox:1.2.3"},
	} {
		out, calls, code := runBash(t, block, append([]string{"VERSION=v1.2.3", "TAGS=1.2.3 1.2"}, c.env...)...)
		if code == 0 || !strings.Contains(out, c.name) {
			t.Errorf("%v: exit %d, output %q", c.env, code, out)
		}
		for _, call := range calls {
			if f := strings.Fields(call); len(f) > 0 && slices.Contains([]string{"push", "tag", "rmi", "login", "buildx"}, f[0]) {
				t.Errorf("%v: verify ran docker %s", c.env, call)
			}
		}
	}
	text := readRepo(t, workflowPath)
	if strings.Contains(text, "download-artifact") {
		t.Error("the workflow downloads an artifact")
	}
}
