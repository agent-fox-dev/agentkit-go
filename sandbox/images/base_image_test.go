package images

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// inspectJSON decodes `engine image inspect --format '{{json <format>}}' ref`.
func inspectJSON(t *testing.T, engine, ref, format string, v any) {
	t.Helper()
	out, se, code := runEngine(t, engine, "image", "inspect", "--format", "{{json "+format+"}}", ref)
	if code != 0 {
		t.Fatalf("image inspect %s: exit %d: %s", ref, code, se)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), v); err != nil {
		t.Fatalf("image inspect %s %s: %v\n%s", ref, format, err, out)
	}
}

// versionLine is what --version prints for version, before the commit and
// build time an image build stamps.
func versionLine(version string) string {
	return fmt.Sprintf("agentkit-sandboxd %s (protocol %d.%d)", version, sandbox.ProtocolMajor, sandbox.ProtocolMinor)
}

// isVersionLine reports whether out is the --version output for version.
func isVersionLine(out, version string) bool {
	rest, ok := strings.CutPrefix(out, versionLine(version))
	return ok && (rest == "\n" || strings.HasPrefix(rest, " ") && strings.HasSuffix(rest, "\n"))
}

// mustExec runs command through proxy execute and fails t on a transport
// error, returning the exit code and output.
func mustExec(t *testing.T, h *harness, command string) (int, string) {
	t.Helper()
	rc, out, err := execIn(context.Background(), h.tools, command)
	if err != nil {
		t.Fatalf("%v; engine stderr: %s", err, h.stderr.String())
	}
	return rc, out
}

// TestBaseImage attaches to one agent started from the base image and runs
// the protocol-level checks against it.
func TestBaseImage(t *testing.T) {
	engine, base := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	h := startT(t, engine, base)
	info := h.sb.Agent()

	// TS-16-38: the agent attaches and reports the expected version,
	// protocol, workspace, tree-sitter backend and git, and its Tools pass
	// the digest check. 16-REQ-7.3, 16-REQ-7.4, 16-REQ-2.5, 16-REQ-2.3.
	t.Run("attach_TS16_38", func(t *testing.T) {
		if err := checkFacts(info, expectedVersion(os.Getenv)); err != nil {
			t.Error(err)
		}
		if info.Workdir != "/workspace" || info.OutlineBackend != "tree-sitter" || !info.Git {
			t.Errorf("Agent() = %+v", info)
		}
		var names []string
		for _, tl := range h.tools {
			names = append(names, tl.Name)
		}
		if !slices.Contains(names, "execute") || !slices.Contains(names, "write_file") {
			t.Errorf("tools %v lack execute or write_file", names)
		}
	})

	// TS-16-39: git, bash and the CA bundle are present, tini is absent,
	// and git trusts every directory. 16-REQ-2.1, 16-REQ-2.4. The UBI base
	// ships rsync and curl, so their absence is not checked
	// (docs/errata/16_sandbox_images.md).
	t.Run("packages_TS16_39", func(t *testing.T) {
		if rc, out := mustExec(t, h, "git --version"); rc != 0 || !strings.HasPrefix(out, "git version") {
			t.Errorf("git --version: rc %d, %q", rc, out)
		}
		for _, c := range []string{"test -x /bin/bash", "test -s /etc/pki/tls/certs/ca-bundle.crt"} {
			if rc, out := mustExec(t, h, c); rc != 0 {
				t.Errorf("%s: rc %d, %q", c, rc, out)
			}
		}
		if rc, out := mustExec(t, h, "command -v tini"); rc == 0 {
			t.Errorf("tini is present: %q", out)
		}
		if rc, out := mustExec(t, h, "git config --system --get-all safe.directory"); rc != 0 || out != "*" {
			t.Errorf("safe.directory: rc %d, %q", rc, out)
		}
	})

	// TS-16-40: the image runs as UBI's arbitrary-UID-friendly 1001:0, owns
	// /workspace and its home with group write, accepts a workspace write,
	// and cannot replace the root-owned agent. 16-REQ-2.2, 16-REQ-2.3,
	// 16-REQ-2.5, as the UBI base changed them
	// (docs/errata/16_sandbox_images.md).
	t.Run("user_TS16_40", func(t *testing.T) {
		for c, want := range map[string]string{
			"id -u": "1001", "id -g": "0",
			"echo $HOME":                           "/home/agentkit",
			"stat -c '%u %g %a' /workspace":        "1001 0 775",
			"stat -c '%u %g %a' /home/agentkit":    "1001 0 775",
			"stat -c '%u %a' " + sandbox.AgentPath: "0 755",
		} {
			if rc, out := mustExec(t, h, c); rc != 0 || out != want {
				t.Errorf("%s: rc %d, %q, want %q", c, rc, out, want)
			}
		}
		if rc, _ := mustExec(t, h, "test -w "+sandbox.AgentPath); rc == 0 {
			t.Error("the agent binary is writable by the tools' user")
		}
		r, err := callTool(context.Background(), h.tools, "write_file", `{"path": "probe.txt", "content": "x"}`)
		if err != nil || r.Error != "" {
			t.Fatalf("write_file: %+v, %v", r, err)
		}
		if rc, out := mustExec(t, h, "cat /workspace/probe.txt; echo; stat -c %u /workspace/probe.txt"); rc != 0 || out != "x\n1001" {
			t.Errorf("probe.txt: rc %d, %q", rc, out)
		}
	})

	// TS-16-43: under Landlock ABI 2 or more the agent denies /var/tmp and
	// allows HOME, /tmp and /workspace. 16-REQ-2.8.
	t.Run("landlock_TS16_43", func(t *testing.T) {
		t.Logf("Landlock ABI %d (kernel %d), TCP %v", info.Landlock.ABI, info.Landlock.KernelABI, info.Landlock.TCP)
		if err := landlockCheck(info, execVia(h), t.Logf); err != nil {
			t.Error(err)
		}
	})
}

// ubiEnv is the environment the UBI s2i-core base sets; the image adds
// only HOME.
var ubiEnv = []string{"container", "SUMMARY", "DESCRIPTION", "NAME", "VERSION", "STI_SCRIPTS_URL",
	"STI_SCRIPTS_PATH", "APP_ROOT", "PATH", "PLATFORM", "HOME"}

// TS-16-41: image inspect shows the base image's numeric user, agent
// entrypoint, workspace workdir, no volumes and no environment beyond the
// UBI base's and HOME. 16-REQ-2.2, 16-REQ-2.3, 16-REQ-2.5, 16-REQ-2.6, as
// the UBI base changed them (docs/errata/16_sandbox_images.md).
func TestBaseImageConfig(t *testing.T) {
	engine, base := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	var cfg struct {
		User       string
		Entrypoint []string
		WorkingDir string
		Volumes    map[string]any
		Env        []string
	}
	inspectJSON(t, engine, base, ".Config", &cfg)
	if cfg.User != "1001:0" || !slices.Equal(cfg.Entrypoint, []string{sandbox.AgentPath}) || cfg.WorkingDir != "/workspace" || len(cfg.Volumes) != 0 {
		t.Errorf("Config %+v", cfg)
	}
	for _, kv := range cfg.Env {
		k, v, _ := strings.Cut(kv, "=")
		if !slices.Contains(ubiEnv, k) {
			t.Errorf("the image sets %s", k)
		}
		if k == "HOME" && v != "/home/agentkit" {
			t.Errorf("HOME=%s", v)
		}
	}
}

// TS-16-42: both images carry the OCI source and licenses values, non-empty
// title and description, and matching version and revision labels.
// 16-REQ-2.7, 16-REQ-3.2.
func TestBaseImageLabels(t *testing.T) {
	engine, base := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	refs := []string{base}
	if overlay := os.Getenv("AGENTKIT_SANDBOX_OVERLAY_IMAGE"); overlay != "" {
		refs = append(refs, overlay)
	}
	var sets []map[string]string
	for _, ref := range refs {
		var l map[string]string
		inspectJSON(t, engine, ref, ".Config.Labels", &l)
		if l["org.opencontainers.image.source"] != "https://github.com/agent-fox-dev/agentkit-go" || l["org.opencontainers.image.licenses"] != "Apache-2.0" ||
			l["org.opencontainers.image.title"] == "" || l["org.opencontainers.image.description"] == "" ||
			l["org.opencontainers.image.version"] != os.Getenv("AGENTKIT_SANDBOX_IMAGE_VERSION") {
			t.Errorf("%s labels %v", ref, l)
		}
		if _, ok := l["org.opencontainers.image.revision"]; !ok {
			t.Errorf("%s has no revision label", ref)
		}
		sets = append(sets, l)
	}
	if len(sets) == 2 {
		for _, k := range []string{"source", "licenses", "version", "revision"} {
			k = "org.opencontainers.image." + k
			if sets[0][k] != sets[1][k] {
				t.Errorf("%s: base %q, overlay %q", k, sets[0][k], sets[1][k])
			}
		}
	}
}

// TS-16-44: images built without a version report (devel) in Hello and in
// --version, and carry an empty version label. 16-REQ-4.5, 16-REQ-1.6.
func TestBaseImageUnstamped(t *testing.T) {
	engine, base := requireImage(t, "AGENTKIT_SANDBOX_BASE_IMAGE")
	_, overlay := requireImage(t, "AGENTKIT_SANDBOX_OVERLAY_IMAGE")
	if v := os.Getenv("AGENTKIT_SANDBOX_IMAGE_VERSION"); v != "" {
		t.Skipf("the images are stamped %s; this test needs an unstamped build (podman build -f containers/<image>/Containerfile with no AGENTKIT_VERSION)", v)
	}
	h := startT(t, engine, base)
	if v := h.sb.Agent().Version; v != "(devel)" {
		t.Errorf("Agent().Version = %q", v)
	}
	for _, ref := range []string{base, overlay} {
		out, se, code := runEngine(t, engine, "run", "--rm", "--entrypoint", sandbox.AgentPath, ref, "--version")
		if code != 0 || !isVersionLine(out, "(devel)") {
			t.Errorf("%s --version: exit %d, %q (stderr %q)", ref, code, out, se)
		}
		var l map[string]string
		inspectJSON(t, engine, ref, ".Config.Labels", &l)
		if l["org.opencontainers.image.version"] != "" {
			t.Errorf("%s version label %q", ref, l["org.opencontainers.image.version"])
		}
	}
}
