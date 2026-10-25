package sandbox

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

func TestImageTagAcceptsOnlyCanonicalReleaseVersions(t *testing.T) {
	accepted := []string{"v0.6.0", "v0.0.0", "v10.20.30", "v1.2.3-rc.1", "v1.0.0-alpha", "v1.2.3-rc.20240102150405", "v1.2.3-x-y.0a"}
	refused := []string{"", "(devel)", "latest", "1.2.3", "V1.2.3", " v1.2.3", "v1.2", "v1", "v1.2.3.4",
		"v01.2.3", "v1.02.3", "v1.2.03", "v1.2.3-", "v1.2.3-rc..1", "v1.2.3-rc.01",
		"v1.2.3+dirty", "v2.0.0+incompatible", "v1.2.3-rc.1+build.5",
		// The three pseudo-version forms.
		"v0.0.0-20240102150405-abcdefabcdef", "v1.2.4-0.20240102150405-abcdefabcdef", "v1.2.3-pre.0.20240102150405-abcdefabcdef"}
	for _, v := range accepted {
		if tag, ok := ImageTag(v); !ok || tag != v[1:] {
			t.Errorf("ImageTag(%q) = (%q, %v), want (%q, true)", v, tag, ok, v[1:])
		}
	}
	for _, v := range refused {
		if tag, ok := ImageTag(v); ok || tag != "" {
			t.Errorf("ImageTag(%q) = (%q, %v), want (\"\", false)", v, tag, ok)
		}
	}
}

func TestImageRefTagsTheRepoOrSaysWhatToDoInstead(t *testing.T) {
	if ref, err := imageRef(BaseImageRepo, "v1.2.3-rc.1"); ref != "quay.io/agentfox/agentkit-sandbox:1.2.3-rc.1" || err != nil {
		t.Errorf("imageRef of a release = %q, %v", ref, err)
	}
	for _, v := range []string{"(devel)", "v1.2.4-0.20240102150405-abcdefabcdef", "v1.2.3+dirty"} {
		ref, err := imageRef(BaseImageRepo, v)
		if ref != "" || !errors.Is(err, ErrNoPublishedImage) {
			t.Errorf("imageRef(%q) = %q, %v; want ErrNoPublishedImage", v, ref, err)
			continue
		}
		for _, want := range []string{v, "Spec.Image", "make build-containers"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("imageRef(%q): %q lacks %q", v, err, want)
			}
		}
	}
	ref, err := DefaultImage()
	if wantRef, wantErr := imageRef(BaseImageRepo, buildver.Version()); ref != wantRef || errors.Is(err, ErrNoPublishedImage) != errors.Is(wantErr, ErrNoPublishedImage) {
		t.Errorf("DefaultImage() = %q, %v; want the image of the build's version %s", ref, err, buildver.Version())
	}
}

// An embedder binary stamped with a release resolves that release's image,
// and an unstamped or pseudo-versioned one gets ErrNoPublishedImage.
func TestDefaultImageFollowsTheEmbeddersStampedVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the embedder program; skipped under -short")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	const pseudo = "v1.2.4-0.20240102150405-abcdefabcdef"
	for _, c := range []struct{ stamp, want string }{
		{"v1.2.3", "quay.io/agentfox/agentkit-sandbox:1.2.3\n"},
		{"", "(devel)"},
		{pseudo, pseudo},
	} {
		bin := filepath.Join(t.TempDir(), "imageref")
		args := []string{"build", "-buildvcs=false", "-o", bin}
		if c.stamp != "" {
			args = append(args, "-ldflags", "-X github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver.stamp="+c.stamp)
		}
		if out, err := exec.Command(goBin, append(args, "./testdata/imageref")...).CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
		out, err := exec.Command(bin).Output()
		var ee *exec.ExitError
		switch {
		case c.stamp == "v1.2.3" && (err != nil || string(out) != c.want):
			t.Errorf("stamped %s: %q, %v; want %q", c.stamp, out, err, c.want)
		case c.stamp != "v1.2.3" && (!errors.As(err, &ee) || ee.ExitCode() != 1 || len(out) != 0 ||
			!strings.Contains(string(ee.Stderr), ErrNoPublishedImage.Error()) || !strings.Contains(string(ee.Stderr), c.want)):
			t.Errorf("stamp %q: %q, %v; want exit 1 and ErrNoPublishedImage naming %s", c.stamp, out, err, c.want)
		}
	}
}
