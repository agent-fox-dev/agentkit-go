package podman

import (
	"fmt"
	"strings"
)

// parseVersion identifies the engine from `<Binary> --version`: output
// beginning "podman version" is podman, which includes a docker shim that
// runs podman, and output beginning "Docker version" is docker.
func parseVersion(out string) (Engine, error) {
	switch {
	case strings.HasPrefix(out, "podman version"):
		return EnginePodman, nil
	case strings.HasPrefix(out, "Docker version"):
		return EngineDocker, nil
	}
	return "", fmt.Errorf("podman: unrecognised engine: --version printed %q, want \"podman version …\" or \"Docker version …\"", out)
}
