package images

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// instr is one Containerfile instruction: its keyword, upper-cased, and its
// arguments with backslash continuations joined.
type instr struct {
	kw, args string
}

// stage is one build stage: the name after AS, lower-cased, its FROM
// arguments and its instructions after FROM.
type stage struct {
	name, from string
	instrs     []instr
}

// parseContainerfile splits text into stages at each FROM, joining
// continuation lines and dropping comments.
func parseContainerfile(text string) []stage {
	var lines []string
	cur := ""
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasSuffix(t, "\\") {
			cur += strings.TrimSuffix(t, "\\") + " "
			continue
		}
		cur += t
		if strings.TrimSpace(cur) != "" {
			lines = append(lines, strings.TrimSpace(cur))
		}
		cur = ""
	}
	var stages []stage
	for _, l := range lines {
		kw, args, _ := strings.Cut(l, " ")
		kw = strings.ToUpper(kw)
		args = strings.TrimSpace(args)
		if kw == "FROM" {
			s := stage{from: args}
			f := strings.Fields(args)
			for i := range f {
				if strings.EqualFold(f[i], "AS") && i+1 < len(f) {
					s.name = strings.ToLower(f[i+1])
				}
			}
			stages = append(stages, s)
			continue
		}
		if len(stages) > 0 {
			stages[len(stages)-1].instrs = append(stages[len(stages)-1].instrs, instr{kw, args})
		}
	}
	return stages
}

// find is the stage named name, or nil.
func find(stages []stage, name string) *stage {
	for i := range stages {
		if stages[i].name == name {
			return &stages[i]
		}
	}
	return nil
}

// all returns the arguments of every instruction kw in s.
func (s *stage) all(kw string) []string {
	var out []string
	for _, in := range s.instrs {
		if in.kw == kw {
			out = append(out, in.args)
		}
	}
	return out
}

// copyArgs is a parsed COPY: its flags and its sources and destination.
type copyArgs struct {
	from, chown, chmod string
	srcs               []string
	dest               string
}

func parseCopy(args string) copyArgs {
	var c copyArgs
	var rest []string
	for _, f := range strings.Fields(args) {
		switch {
		case strings.HasPrefix(f, "--from="):
			c.from = strings.ToLower(strings.TrimPrefix(f, "--from="))
		case strings.HasPrefix(f, "--chown="):
			c.chown = strings.TrimPrefix(f, "--chown=")
		case strings.HasPrefix(f, "--chmod="):
			c.chmod = strings.TrimPrefix(f, "--chmod=")
		default:
			rest = append(rest, f)
		}
	}
	if len(rest) > 0 {
		c.srcs, c.dest = rest[:len(rest)-1], rest[len(rest)-1]
	}
	return c
}

// labels are the key=value pairs of a stage's LABEL instructions, quotes
// stripped.
func (s *stage) labels() map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`([A-Za-z0-9_.-]+)=("([^"]*)"|(\S*))`)
	for _, a := range s.all("LABEL") {
		for _, m := range re.FindAllStringSubmatch(a, -1) {
			v := m[4]
			if strings.HasPrefix(m[2], `"`) {
				v = m[3]
			}
			out[m[1]] = v
		}
	}
	return out
}

// entrypoint decodes a JSON-form ENTRYPOINT.
func (s *stage) entrypoint() []string {
	var ep []string
	for _, a := range s.all("ENTRYPOINT") {
		_ = json.Unmarshal([]byte(a), &ep)
	}
	return ep
}

// buildStage is the stage whose go build sets CGO_ENABLED=<cgo>, or nil.
func buildStage(stages []stage, cgo string) *stage {
	for i := range stages {
		for _, r := range stages[i].all("RUN") {
			if strings.Contains(r, "CGO_ENABLED="+cgo) && strings.Contains(r, "go build") {
				return &stages[i]
			}
		}
	}
	return nil
}

// repoRoot is the repository root, found from the test's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "sandbox", "images")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no repository root above the test")
		}
		dir = parent
	}
}

func readRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// goDirective is the go version go.mod declares.
func goDirective(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^go (\S+)$`).FindStringSubmatch(readRepo(t, "go.mod"))
	if m == nil {
		t.Fatal("go.mod declares no go version")
	}
	return m[1]
}

func realStages(t *testing.T) []stage {
	t.Helper()
	return parseContainerfile(readRepo(t, "sandbox/images/Containerfile"))
}

// TS-16-13: the build stages run on the build platform with a local
// toolchain, download modules first, and compile with the required flags
// and no extra environment. 16-REQ-1.2, 16-REQ-1.3, 16-REQ-1.6. The agent
// is built in the nested sandbox module (docs/errata/16_sandbox_images.md).
func TestBuildStages_TS16_13(t *testing.T) {
	text := readRepo(t, "sandbox/images/Containerfile")
	stages := parseContainerfile(text)
	ldflags := `-ldflags "-s -w -X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp=${AGENTKIT_VERSION}"`
	var builds []stage
	for _, s := range stages {
		if s.name != "base" && s.name != "overlay" {
			builds = append(builds, s)
		}
	}
	if len(builds) == 0 {
		t.Fatal("no build stage")
	}
	for _, s := range builds {
		if want := "--platform=$BUILDPLATFORM golang:" + goDirective(t) + "-trixie AS " + s.name; !strings.EqualFold(s.from, want) {
			t.Errorf("stage %s: FROM %s, want FROM %s", s.name, s.from, want)
		}
		if !slices.ContainsFunc(s.all("ENV"), func(e string) bool { return strings.Contains(e, "GOTOOLCHAIN=local") }) {
			t.Errorf("stage %s does not set GOTOOLCHAIN=local", s.name)
		}
		modsAt, downloadAt, restAt := -1, -1, -1
		for i, in := range s.instrs {
			switch {
			case in.kw == "COPY" && strings.Contains(in.args, "go.mod") && strings.Contains(in.args, "go.sum") && modsAt < 0:
				modsAt = i
			case in.kw == "RUN" && strings.Contains(in.args, "go mod download") && downloadAt < 0:
				downloadAt = i
			case in.kw == "COPY" && strings.HasPrefix(in.args, ". ") && restAt < 0:
				restAt = i
			}
		}
		if modsAt < 0 || downloadAt < modsAt || restAt < downloadAt {
			t.Errorf("stage %s: go.mod at %d, go mod download at %d, the source at %d", s.name, modsAt, downloadAt, restAt)
		}
	}
	for _, cgo := range []string{"1", "0"} {
		s := buildStage(stages, cgo)
		if s == nil {
			t.Errorf("no stage builds with CGO_ENABLED=%s", cgo)
			continue
		}
		var build string
		for _, r := range s.all("RUN") {
			if strings.Contains(r, "go build") {
				build = r
			}
		}
		for _, want := range []string{"cd sandbox", "GOOS=linux", "GOARCH=$TARGETARCH", "go build -trimpath -buildvcs=false", ldflags, "./cmd/agentkit-sandboxd"} {
			if !strings.Contains(build, want) {
				t.Errorf("the CGO_ENABLED=%s build lacks %q: %s", cgo, want, build)
			}
		}
	}
	argDefault := regexp.MustCompile(`(?m)^ARG (AGENTKIT_VERSION|AGENTKIT_REVISION)(=(.*))?$`)
	args := map[string]bool{}
	for _, m := range argDefault.FindAllStringSubmatch(text, -1) {
		args[m[1]] = true
		if m[3] != "" {
			t.Errorf("ARG %s has the default %q", m[1], m[3])
		}
	}
	if !args["AGENTKIT_VERSION"] || !args["AGENTKIT_REVISION"] {
		t.Errorf("ARG declarations %v", args)
	}
	if strings.Contains(text, "CGO_LDFLAGS_ALLOW") {
		t.Error("the Containerfile sets CGO_LDFLAGS_ALLOW")
	}
	allowed := []string{"GOTOOLCHAIN", "CGO_ENABLED", "GOOS", "GOARCH", "CC"}
	for _, s := range stages {
		for _, e := range s.all("ENV") {
			for _, kv := range strings.Fields(e) {
				if k, _, _ := strings.Cut(kv, "="); !slices.Contains(allowed, k) {
					t.Errorf("stage %s sets ENV %s", s.name, k)
				}
			}
		}
	}
	for _, m := range regexp.MustCompile(`export (\w+)=`).FindAllStringSubmatch(text, -1) {
		if !slices.Contains(allowed, m[1]) {
			t.Errorf("the Containerfile exports %s", m[1])
		}
	}
}

// TS-16-14: the cgo stage installs the cross toolchain only when target and
// build arch differ, and both build stages reject unsupported
// architectures by name. 16-REQ-1.4, 16-REQ-1.5.
func TestCrossAndUnsupportedArch_TS16_14(t *testing.T) {
	stages := realStages(t)
	cgo := buildStage(stages, "1")
	if cgo == nil {
		t.Fatal("no cgo stage")
	}
	runs := strings.Join(cgo.all("RUN"), "\n")
	cross := regexp.MustCompile(`if \[ "\$TARGETARCH" != "\$BUILDARCH" \].*crossbuild-essential-\$\{?TARGETARCH\}?`)
	if !cross.MatchString(runs) {
		t.Error("the cgo stage does not install crossbuild-essential-$TARGETARCH under a TARGETARCH != BUILDARCH condition")
	}
	for _, want := range []string{"amd64) export CC=x86_64-linux-gnu-gcc", "arm64) export CC=aarch64-linux-gnu-gcc"} {
		if !strings.Contains(runs, want) {
			t.Errorf("the cgo stage lacks %q", want)
		}
	}
	for _, cg := range []string{"1", "0"} {
		s := buildStage(stages, cg)
		guard, build := -1, -1
		for i, in := range s.instrs {
			if in.kw != "RUN" {
				continue
			}
			if strings.Contains(in.args, `case "$TARGETARCH"`) && strings.Contains(in.args, "*)") &&
				strings.Contains(in.args, "linux/amd64") && strings.Contains(in.args, "linux/arm64") && strings.Contains(in.args, "exit 1") && guard < 0 {
				guard = i
			}
			if strings.Contains(in.args, "go build") {
				build = i
			}
		}
		if guard < 0 || guard > build {
			t.Errorf("stage %s: the unsupported-arch guard at %d, the go build at %d", s.name, guard, build)
		}
	}
}

// TS-16-16: the base stage starts from trixie-slim, installs exactly git,
// bash and ca-certificates, runs as numeric 1000:1000 in /workspace, and
// declares no ENV or VOLUME. 16-REQ-2.1, 16-REQ-2.2, 16-REQ-2.3, 16-REQ-2.6.
func TestBaseStage_TS16_16(t *testing.T) {
	text := readRepo(t, "sandbox/images/Containerfile")
	stages := parseContainerfile(text)
	base := find(stages, "base")
	if base == nil {
		t.Fatal("no base stage")
	}
	if base.from != "debian:trixie-slim AS base" {
		t.Errorf("base FROM %s", base.from)
	}
	installs := 0
	for _, r := range base.all("RUN") {
		i := strings.Index(r, "apt-get install")
		if i < 0 {
			continue
		}
		installs++
		cmd, _, _ := strings.Cut(r[i:], "&&")
		var pkgs []string
		for _, f := range strings.Fields(cmd)[2:] {
			if !strings.HasPrefix(f, "-") {
				pkgs = append(pkgs, f)
			}
		}
		slices.Sort(pkgs)
		if !strings.Contains(cmd, "--no-install-recommends") || !slices.Equal(pkgs, []string{"bash", "ca-certificates", "git"}) {
			t.Errorf("apt-get install %q installs %v", cmd, pkgs)
		}
		if !strings.Contains(r, "rm -rf /var/lib/apt/lists") {
			t.Error("the install RUN does not remove /var/lib/apt/lists")
		}
	}
	if installs != 1 {
		t.Errorf("%d apt-get installs in base, want 1", installs)
	}
	i := strings.Index(text, "FROM debian:trixie-slim AS base")
	j := strings.Index(text[i+1:], "\nFROM ")
	stageText := text[i:]
	if j >= 0 {
		stageText = text[i : i+1+j]
	}
	for _, tok := range []string{"rsync", "curl", "tini"} {
		if strings.Contains(stageText, tok) {
			t.Errorf("the base stage mentions %s", tok)
		}
	}
	if !slices.Equal(base.all("USER"), []string{"1000:1000"}) || !slices.Equal(base.all("WORKDIR"), []string{"/workspace"}) {
		t.Errorf("USER %v, WORKDIR %v", base.all("USER"), base.all("WORKDIR"))
	}
	for _, s := range stages {
		if len(s.all("VOLUME")) > 0 {
			t.Errorf("stage %s declares a VOLUME", s.name)
		}
		if (s.name == "base" || s.name == "overlay") && len(s.all("ENV")) > 0 {
			t.Errorf("stage %s declares ENV", s.name)
		}
	}
}

// TS-16-17: the overlay stage is a single COPY onto scratch with the agent
// ENTRYPOINT, and both stages carry the same OCI labels. 16-REQ-3.1,
// 16-REQ-3.2, 16-REQ-2.7.
func TestOverlayStageAndLabels_TS16_17(t *testing.T) {
	stages := realStages(t)
	base, overlay := find(stages, "base"), find(stages, "overlay")
	if base == nil || overlay == nil {
		t.Fatal("no base or overlay stage")
	}
	if len(overlay.all("RUN"))+len(overlay.all("ADD")) > 0 || len(overlay.all("COPY")) != 1 {
		t.Fatalf("overlay instructions %+v", overlay.instrs)
	}
	c := parseCopy(overlay.all("COPY")[0])
	static := buildStage(stages, "0")
	if static == nil || c.from != static.name || c.dest != sandbox.AgentPath ||
		(c.chown != "" && c.chown != "0:0") || (c.chmod != "" && c.chmod != "755" && c.chmod != "0755") {
		t.Errorf("overlay COPY %+v", c)
	}
	if ep := overlay.entrypoint(); !slices.Equal(ep, []string{sandbox.AgentPath}) {
		t.Errorf("overlay ENTRYPOINT %v", ep)
	}
	bl, ol := base.labels(), overlay.labels()
	for name, l := range map[string]map[string]string{"base": bl, "overlay": ol} {
		if l["org.opencontainers.image.source"] != "https://github.com/agent-fox-dev/agentkit-go" ||
			l["org.opencontainers.image.licenses"] != "Apache-2.0" ||
			l["org.opencontainers.image.title"] == "" || l["org.opencontainers.image.description"] == "" ||
			l["org.opencontainers.image.version"] != "${AGENTKIT_VERSION}" || l["org.opencontainers.image.revision"] != "${AGENTKIT_REVISION}" {
			t.Errorf("%s labels %v", name, l)
		}
	}
	if len(bl) != len(ol) {
		t.Errorf("base labels %v, overlay labels %v", bl, ol)
	}
	for k, v := range bl {
		if ol[k] != v {
			t.Errorf("label %s: base %q, overlay %q", k, v, ol[k])
		}
	}
}
