package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-19-57: usage errors exit 2 with one stderr line, before the token
// file is read or the engine contacted. 19-REQ-8.5.
func TestUsageErrors_TS19_57(t *testing.T) {
	log := fakeEngine(t)
	unreadable := filepath.Join(t.TempDir(), "missing-tokens")
	for _, args := range [][]string{
		{"--no-such-flag"},
		{"--tls-cert", "c.pem"},
		{"--plaintext", "--tls-cert", "c.pem", "--tls-key", "k.pem"},
		{},
		{"--plaintext", "--engine", "kube"},
		{"--plaintext", "--runtime", "runc"},
		{"--plaintext", "--network", "none,allowlist"},
		{"--plaintext", "--max-sandboxes", "0"},
		{"--plaintext", "--max-ttl", "0s"},
		{"--plaintext", "--reap-interval", "-1m"},
		{"--plaintext", "extra"},
	} {
		code, _, stderr := runCmd(append([]string{"--token-file", unreadable}, args...)...)
		lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
		if code != 2 || len(lines) != 1 || !strings.HasPrefix(stderr, "agentkit-gateway: ") {
			t.Errorf("%v: exit %d, stderr %q; want 2 and one agentkit-gateway: line", args, code, stderr)
		}
	}
	if code, _, stderr := runCmd("--plaintext"); code != 2 || !strings.Contains(stderr, "--token-file") {
		t.Errorf("no --token-file: exit %d, %q", code, stderr)
	}
	if b, err := os.ReadFile(log); err == nil && len(b) > 0 {
		t.Errorf("the engine was run for a usage error: %s", b)
	}
}
