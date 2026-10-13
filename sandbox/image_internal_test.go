package sandbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

// TS-16-7: the repository constants and error text are exact, and the
// reference builder joins repository and tag for a release version.
// 16-REQ-5.1, 16-REQ-5.5.
func TestImageRefs_TS16_7(t *testing.T) {
	if BaseImageRepo != "ghcr.io/agent-fox-dev/agentkit-sandbox" || OverlayImageRepo != "ghcr.io/agent-fox-dev/agentkit-sandboxd" {
		t.Errorf("repositories %q, %q", BaseImageRepo, OverlayImageRepo)
	}
	if ErrNoPublishedImage.Error() != "sandbox: no published image for this agentkit-go version" {
		t.Errorf("ErrNoPublishedImage = %q", ErrNoPublishedImage)
	}
	for _, c := range []struct{ version, tag string }{{"v1.2.3", "1.2.3"}, {"v1.2.3-rc.1", "1.2.3-rc.1"}} {
		for _, repo := range []string{BaseImageRepo, OverlayImageRepo} {
			if ref, err := imageRef(repo, c.version); ref != repo+":"+c.tag || err != nil {
				t.Errorf("imageRef(%s, %s) = %q, %v", repo, c.version, ref, err)
			}
		}
	}
	type result struct {
		ref string
		err string
	}
	of := func(ref string, err error) result {
		if err == nil {
			return result{ref, ""}
		}
		return result{ref, err.Error()}
	}
	v := buildver.Version()
	if got, want := of(DefaultImage()), of(imageRef(BaseImageRepo, v)); got != want {
		t.Errorf("DefaultImage() = %+v, want %+v", got, want)
	}
	if got, want := of(OverlayImage()), of(imageRef(OverlayImageRepo, v)); got != want {
		t.Errorf("OverlayImage() = %+v, want %+v", got, want)
	}
}

// TS-16-8: a version with no published image yields an empty reference and
// an error wrapping ErrNoPublishedImage that tells the embedder what to do.
// 16-REQ-5.6.
func TestNoPublishedImage_TS16_8(t *testing.T) {
	for _, v := range []string{"(devel)", "", "v1.2.4-0.20240102150405-abcdefabcdef", "v1.2.3+dirty"} {
		for _, repo := range []string{BaseImageRepo, OverlayImageRepo} {
			ref, err := imageRef(repo, v)
			if ref != "" || !errors.Is(err, ErrNoPublishedImage) {
				t.Errorf("imageRef(%s, %q) = %q, %v", repo, v, ref, err)
				continue
			}
			for _, want := range []string{v, "Spec.Image", "make sandbox-images"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("imageRef(%s, %q): %q lacks %q", repo, v, err, want)
				}
			}
		}
	}
}
