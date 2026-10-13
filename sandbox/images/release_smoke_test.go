package images

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

const releaseVar = "AGENTKIT_SANDBOX_RELEASE_VERSION"

// TS-16-53: a published release vX.Y.Z has both images at X.Y.Z and X.Y for
// linux/amd64 and linux/arm64, pullable anonymously, whose agent reports
// vX.Y.Z. Opt-in with AGENTKIT_SANDBOX_RELEASE_VERSION=vX.Y.Z once the tag
// has been pushed and both GHCR packages are public. 16-PATH-3,
// 16-REQ-9.6, 16-REQ-10.2, 16-REQ-1.4.
func TestPublishedRelease_TS16_53(t *testing.T) {
	if os.Getenv(nestedVar) != "" {
		t.Skip("nested run")
	}
	version := os.Getenv(releaseVar)
	if version == "" {
		t.Skip(releaseVar + " is not set; set it to a published vX.Y.Z to verify the release")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH; this test uses docker pull and docker buildx imagetools")
	}
	tag, ok := sandbox.ImageTag(version)
	if !ok {
		t.Fatalf("%s=%q is not a release version", releaseVar, version)
	}
	tags := []string{tag}
	if !strings.Contains(tag, "-") {
		tags = append(tags, tag[:strings.LastIndex(tag, ".")])
	}
	// Anonymous: no credentials from the user's docker configuration.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	for _, repo := range []string{sandbox.BaseImageRepo, sandbox.OverlayImageRepo} {
		for _, tg := range tags {
			ref := repo + ":" + tg
			so, se, code := runEngine(t, "docker", "buildx", "imagetools", "inspect", ref)
			if code != 0 {
				t.Errorf("imagetools inspect %s: exit %d: %s", ref, code, se)
				continue
			}
			for _, p := range []string{"linux/amd64", "linux/arm64"} {
				if !strings.Contains(so, p) {
					t.Errorf("%s does not list %s:\n%s", ref, p, so)
				}
				if _, se, code := runEngine(t, "docker", "pull", "--platform", p, ref); code != 0 {
					t.Errorf("anonymous pull of %s for %s: exit %d: %s", ref, p, code, se)
					continue
				}
				out, se, code := runEngine(t, "docker", "run", "--rm", "--platform", p, "--entrypoint", sandbox.AgentPath, ref, "--version")
				if want := versionLine(version); code != 0 || out != want {
					t.Errorf("%s (%s) --version: exit %d, %q, want %q; stderr %s", ref, p, code, out, want, se)
				}
			}
		}
	}
}
