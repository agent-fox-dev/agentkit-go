package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// A usage error exits 2 with one stderr line, before the token file is read
// or the backend made; a token in the environment is no --token-file.
func TestAUsageErrorExitsTwoBeforeAnythingElse(t *testing.T) {
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", tok40)
	for _, args := range [][]string{
		{"--no-such-flag"}, {"--tls-cert", "c.pem"}, {"--plaintext", "--tls-cert", "c.pem", "--tls-key", "k.pem"}, {},
		{"--plaintext", "--engine", "kube"}, {"--plaintext", "--runtime", "runc"}, {"--plaintext", "--network", "none,allowlist"},
		{"--plaintext", "--max-sandboxes", "0"}, {"--plaintext", "--max-ttl", "0s"}, {"--plaintext", "--reap-interval", "-1m"},
		{"--plaintext", "extra"}, {"--plaintext", "--token-file", ""},
	} {
		made := false
		var stderr bytes.Buffer
		code := run(append([]string{"--token-file", "/nonexistent/tokens"}, args...), io.Discard, &stderr,
			deps{newBackend: func(podman.Options) (sandbox.Provider, error) { made = true; return nil, errors.New("no") }})
		if lines := strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n"); code != 2 || len(lines) != 1 || !strings.HasPrefix(lines[0], "agentkit-gateway: ") || made {
			t.Errorf("%v: exit %d, stderr %q, backend made %v; want 2 and one agentkit-gateway: line, before the backend", args, code, stderr.String(), made)
		}
	}
}
