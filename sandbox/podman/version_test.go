package podman

import "testing"

// TS-17-12: the parser identifies podman, docker and a docker shim that
// runs podman. 17-REQ-2.3.
func TestParseVersion_TS17_12(t *testing.T) {
	for out, want := range map[string]Engine{
		"podman version 5.2.1\n":                       EnginePodman,
		"podman version 4.9.3":                         EnginePodman,
		"Docker version 24.0.7, build afdd53b\n":       EngineDocker,
		"Docker version 20.10.24+dfsg1, build 297e128": EngineDocker,
	} {
		if got, err := parseVersion(out); got != want || err != nil {
			t.Errorf("parseVersion(%q) = %q, %v; want %q, nil", out, got, err, want)
		}
	}
}
