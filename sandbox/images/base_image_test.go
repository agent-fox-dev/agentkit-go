package images

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The agent started from the image attaches and reports its version,
// protocol, workspace, tree-sitter outline backend and git.
func TestBaseImageAgentAttaches(t *testing.T) {
	engine, image := baseImage(t)
	a := startAgent(t, engine, image)
	info := a.sb.Agent()
	if info.Version != expectedVersion() || info.ProtocolMajor != sandbox.ProtocolMajor || info.Workdir != "/workspace" ||
		info.OutlineBackend != "tree-sitter" || !info.Git {
		t.Errorf("Agent() = %+v, want version %s, protocol major %d", info, expectedVersion(), sandbox.ProtocolMajor)
	}
	for _, name := range []string{"execute", "write_file"} {
		if !slices.ContainsFunc(a.tools, func(tl core.Tool) bool { return tl.Name == name }) {
			t.Errorf("the tools lack %s", name)
		}
	}
}

func TestBaseImageAgentAnswersVersion(t *testing.T) {
	engine, image := baseImage(t)
	out, err := exec.Command(engine, "run", "--rm", "--entrypoint", sandbox.AgentPath, image, "--version").Output()
	want := fmt.Sprintf("agentkit-sandboxd %s (protocol %d.%d)", expectedVersion(), sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	rest, ok := strings.CutPrefix(string(out), want)
	if err != nil || !ok || rest != "\n" && !(strings.HasPrefix(rest, " ") && strings.HasSuffix(rest, "\n")) {
		t.Errorf("--version = %q, %v; want %q, then the commit and build time", out, err, want)
	}
}

// git, bash and the CA bundle are there; rsync, curl, tini and the apt
// lists are not; git trusts every directory.
func TestBaseImageShipsOnlyTheToolsItNeeds(t *testing.T) {
	engine, image := baseImage(t)
	a := startAgent(t, engine, image)
	for command, want := range map[string]int{
		"git --version | grep -q '^git version'": 0, "test -x /bin/bash": 0, "test -s /etc/ssl/certs/ca-certificates.crt": 0,
		"command -v rsync": 1, "command -v curl": 1, "command -v tini": 1, "ls /var/lib/apt/lists | grep -q _Packages": 1,
		"test \"$(git config --system --get-all safe.directory)\" = '*'": 0,
	} {
		if rc, out := a.exec(t, command); (rc == 0) != (want == 0) {
			t.Errorf("%s: rc %d, %q", command, rc, out)
		}
	}
}

// The tools run as agentkit 1000:1000, who owns /workspace and its home
// and can write the workspace, but not the root-owned agent.
func TestBaseImageRunsAsAgentkit(t *testing.T) {
	engine, image := baseImage(t)
	a := startAgent(t, engine, image)
	for command, want := range map[string]string{
		"id -u; id -g": "1000\n1000",
		"getent passwd agentkit | cut -d: -f3,4,6,7":          "1000:1000:/home/agentkit:/bin/bash",
		"getent group agentkit | cut -d: -f3":                 "1000",
		"stat -c '%u %g %a' /workspace":                       "1000 1000 755",
		"stat -c '%u %g' /home/agentkit":                      "1000 1000",
		"stat -c '%u %a' " + sandbox.AgentPath:                "0 755",
		"test -w " + sandbox.AgentPath + " || echo read-only": "read-only",
	} {
		if rc, out := a.exec(t, command); rc != 0 || out != want {
			t.Errorf("%s: rc %d, %q, want %q", command, rc, out, want)
		}
	}
	if r := a.call(t, "write_file", map[string]string{"path": "probe.txt", "content": "x"}); r.Error != "" {
		t.Fatalf("write_file: %+v", r)
	}
	if rc, out := a.exec(t, "cat /workspace/probe.txt; echo; stat -c %u /workspace/probe.txt"); rc != 0 || out != "x\n1000" {
		t.Errorf("probe.txt: rc %d, %q", rc, out)
	}
}

// Under Landlock ABI 2 or more the agent denies /var/tmp and allows HOME,
// /tmp and /workspace.
func TestBaseImageLandlockConfinesTheTools(t *testing.T) {
	engine, image := baseImage(t)
	a := startAgent(t, engine, image)
	if l := a.sb.Agent().Landlock; l.ABI < sandbox.LandlockMinABI {
		t.Skipf("the agent applied Landlock ABI %d: %s", l.ABI, l.Note)
	}
	if rc, _ := a.exec(t, "touch /var/tmp/agentkit-probe"); rc == 0 {
		t.Error("touch /var/tmp succeeded under the agent's Landlock ruleset")
	}
	if rc, out := a.exec(t, `touch "$HOME/p" /tmp/p /workspace/p`); rc != 0 {
		t.Errorf("touch in HOME, /tmp and /workspace: rc %d, %q", rc, out)
	}
}

// The image runs the agent as the numeric user in /workspace, with no
// volumes, no environment but PATH, and the OCI labels of its source,
// licence and version.
func TestBaseImageConfigAndLabels(t *testing.T) {
	engine, image := baseImage(t)
	var cfg struct {
		User, WorkingDir string
		Entrypoint, Env  []string
		Volumes          map[string]any
		Labels           map[string]string
	}
	inspect(t, engine, image, ".Config", &cfg)
	if cfg.User != "1000:1000" || !slices.Equal(cfg.Entrypoint, []string{sandbox.AgentPath}) || cfg.WorkingDir != "/workspace" || len(cfg.Volumes) != 0 {
		t.Errorf("Config %+v", cfg)
	}
	for _, kv := range cfg.Env {
		if k, _, _ := strings.Cut(kv, "="); k != "PATH" {
			t.Errorf("the image sets %s", k)
		}
	}
	l := cfg.Labels
	if l["org.opencontainers.image.source"] != "https://github.com/agent-fox-dev/agentkit-go" || l["org.opencontainers.image.licenses"] != "Apache-2.0" ||
		l["org.opencontainers.image.title"] == "" || l["org.opencontainers.image.description"] == "" ||
		l["org.opencontainers.image.version"] != os.Getenv("AGENTKIT_SANDBOX_IMAGE_VERSION") {
		t.Errorf("labels %v", l)
	}
	if _, ok := l["org.opencontainers.image.revision"]; !ok {
		t.Errorf("no revision label in %v", l)
	}
}
