//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/examples/tools/toolcli"
)

// The program's test needs no key and no network: the index is local.
func TestCodeSearch(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package pkg\n\n// Retry runs fn until it succeeds.\nfunc Retry(fn func() error) error {\n\treturn fn()\n}\n"
	if err := os.WriteFile(filepath.Join(ws, "pkg", "retry.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-schema"}, toolcli.ExitOK, "name: code_search\n"},
		{[]string{`{"query":"sym:Retry"}`}, toolcli.ExitOK, "4: func Retry("},
		{[]string{`{"query":"("}`}, toolcli.ExitError, "invalid_arguments"},
		{[]string{`{"query":"Retry","path":"../elsewhere"}`}, toolcli.ExitError, "path_not_allowed"},
	}
	for _, c := range cases {
		var stdout, stderr bytes.Buffer
		code := toolcli.Run(context.Background(), "code_search", withIndex,
			append([]string{"-dir", ws}, c.args...), strings.NewReader(""), &stdout, &stderr)
		if code != c.code || !strings.Contains(stdout.String(), c.want) {
			t.Errorf("%v: exit %d (want %d), want %q in stdout\nstdout:\n%s\nstderr:\n%s",
				c.args, code, c.code, c.want, stdout.String(), stderr.String())
		}
	}
}
