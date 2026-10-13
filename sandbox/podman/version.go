package podman

import (
	"fmt"
	"strings"
)

// parseVersion identifies the engine from `<Binary> --version`: output
// beginning "podman version" is podman, which includes a docker shim that
// runs podman, and output beginning "Docker version" is docker. The output
// carries stderr too, so a line before the version line, such as a docker
// shim's "Emulate Docker CLI using podman" banner or a WARN line, is
// skipped.
func parseVersion(out string) (Engine, error) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "podman version"):
			return EnginePodman, nil
		case strings.HasPrefix(line, "Docker version"):
			return EngineDocker, nil
		}
	}
	return "", fmt.Errorf("podman: unrecognised engine: --version printed %q, want \"podman version …\" or \"Docker version …\"", out)
}
