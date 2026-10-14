package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The example runs offline, over a gateway it starts in process on a
// loopback port, both as a program and in process.
func TestExampleRunsOffline(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	out, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Gateway example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
	var b strings.Builder
	if err := run(context.Background(), &b, io.Discard, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"backend local", "a wrong token: refused", "on gateway/local", "labels map[example:gateway], expires in 10m0s",
		"TODOs: 2", "src: main.go, util.go", "rm: blocked_by_policy", "exported src: main.go, util.go", "the gateway holds 0 sandboxes"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
}

// firstToken reads agentkit-gateway's token file format: the first line
// that is neither blank nor a comment, trimmed.
func TestFirstToken(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ body, want, err string }{
		{"# a comment\n\n  first-token-0123456789abcdefghijklmnop  \nsecond\n", "first-token-0123456789abcdefghijklmnop", ""},
		{"# only a comment\n\n", "", "holds no token"},
	} {
		path := filepath.Join(dir, "tokens")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := firstToken(path)
		if got != c.want || (c.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), c.err)) {
			t.Errorf("firstToken(%q) = %q, %v; want %q, an error containing %q", c.body, got, err, c.want, c.err)
		}
	}
	if _, err := firstToken(""); err == nil || !strings.Contains(err.Error(), "--url needs --token-file") {
		t.Errorf("firstToken(\"\") = %v, want the missing-flag error", err)
	}
}
