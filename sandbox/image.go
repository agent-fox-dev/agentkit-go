package sandbox

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
)

// BaseImageRepo is the published base image: UBI with git, bash and
// ca-certificates, a uid-1001 user and the cgo-built agent at AgentPath. An
// embedder's own image is built FROM it.
const BaseImageRepo = "quay.io/agentfox/agentkit-sandbox"

// ErrNoPublishedImage is returned by DefaultImage when the build's version
// has no published image: (devel), a pseudo-version, or a version with build
// metadata.
var ErrNoPublishedImage = errors.New("sandbox: no published image for this agentkit-go version")

var (
	// release is a canonical release version, with an optional prerelease.
	release = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?$`)
	// pseudoVersion is the timestamp-revision ending of a Go pseudo-version's
	// prerelease, in all three of its forms.
	pseudoVersion = regexp.MustCompile(`(^|\.)[0-9]{14}-[0-9a-f]{12}$`)
)

// ImageTag is the image tag of a release version: version without its
// leading v. It accepts only v MAJOR.MINOR.PATCH with no leading zeros and
// an optional -prerelease of dot-separated identifiers, and refuses (devel),
// build metadata, Go pseudo-versions and anything else, with ("", false).
func ImageTag(version string) (tag string, ok bool) {
	m := release.FindStringSubmatch(version)
	if m == nil {
		return "", false
	}
	if pre := m[4]; pre != "" {
		if pseudoVersion.MatchString(pre) {
			return "", false
		}
		for _, id := range strings.Split(pre, ".") {
			numeric := strings.Trim(id, "0123456789") == ""
			if id == "" || (numeric && len(id) > 1 && id[0] == '0') {
				return "", false
			}
		}
	}
	return version[1:], true
}

// imageRef is repo tagged with version's image tag, or an error wrapping
// ErrNoPublishedImage that says what to do instead.
func imageRef(repo, version string) (string, error) {
	tag, ok := ImageTag(version)
	if !ok {
		return "", fmt.Errorf("%w: agentkit-go version %q has no published sandbox image; set Spec.Image, or build an image with make build-containers",
			ErrNoPublishedImage, version)
	}
	return repo + ":" + tag, nil
}

// DefaultImage is the base image published for this build's version, at the
// exact X.Y.Z tag (never latest or X.Y): the Tools digest check needs the
// host and the agent at the same version.
func DefaultImage() (string, error) { return imageRef(BaseImageRepo, buildver.Version()) }
