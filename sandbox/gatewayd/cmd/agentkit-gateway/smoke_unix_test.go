//go:build !windows

package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// TS-19-76 (smoke, 19-PATH-5): the agentkit-gateway command starts against
// a fake engine, accepts a Dial reporting gateway/podman and exits 0 on
// SIGTERM. 19-REQ-8.1, 19-REQ-8.4.
func TestSmokeCommand_TS19_76(t *testing.T) {
	fakeEngine(t)
	tokens := writeFile(t, t.TempDir(), "tokens", tok40+"\n", 0o600)
	s := startBinary(t, "--token-file", tokens, "--plaintext", "--listen", "127.0.0.1:0")
	addr := waitListening(t, s.stderr)
	line := regexp.MustCompile(`(?m)^agentkit-gateway: listening on 127\.0\.0\.1:\d+ \(plaintext\), backend podman, version \S+$`)
	if !line.MatchString(s.stderr.String()) {
		t.Errorf("the listening line does not read as specified:\n%s", s.stderr)
	}
	p, err := gateway.Dial(context.Background(), "http://"+addr, gateway.Options{Token: tok40})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Capabilities().Provider; got != "gateway/podman" {
		t.Errorf("Capabilities().Provider = %q, want gateway/podman", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if code := s.stop(t); code != 0 {
		t.Errorf("exit %d after SIGTERM, want 0; stderr:\n%s", code, s.stderr)
	}
}
