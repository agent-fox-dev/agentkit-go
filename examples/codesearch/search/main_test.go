//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The example's test needs no key and no network: the index is local.
func TestSearch(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("pkg/retry.go", "package pkg\n\n// Retry runs fn until it succeeds.\nfunc Retry(fn func() error) error {\n\treturn fn()\n}\n")
	mk("pkg/retry_test.go", "package pkg\n\n// TestRetry calls Retry.\nfunc TestRetry() { _ = Retry(nil) }\n")
	mk("cmd/main.go", "package main\n\nfunc main() {}\n")

	cases := []struct {
		name string
		args []string
		want []string
		not  []string
	}{
		{
			name: "symbol query ranks the declaration",
			args: []string{"sym:Retry"},
			want: []string{"## sym:Retry", "pkg/retry.go", "4: func Retry("},
		},
		{
			name: "file filters compose",
			args: []string{`Retry -file:_test`},
			want: []string{"pkg/retry.go"},
			not:  []string{"retry_test.go"},
		},
		{
			name: "path restricts every query",
			args: []string{"--path", "cmd", "func"},
			want: []string{"cmd/main.go"},
			not:  []string{"pkg/"},
		},
		{
			name: "a bad query is a readable result, not a failure",
			args: []string{"bad("},
			want: []string{"invalid_arguments: query parse error"},
		},
		{
			name: "a path outside the workspace is refused",
			args: []string{"--path", "..", "x"},
			want: []string{"path_not_allowed"},
		},
		{
			name: "json prints the structured result",
			args: []string{"--json", "sym:Retry"},
			want: []string{`"files_indexed": 3`, `"path": "pkg/retry.go"`},
		},
		{
			name: "symbol lookup",
			args: []string{"--symbol", "Retry", "--exact", "--kind", "func"},
			want: []string{`1 matches for "Retry"`, "pkg/retry.go:4"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--dir", root}, tc.args...)
			if err := run(args, &stdout, &stderr); err != nil {
				t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
			}
			out := stdout.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(out, n) {
					t.Errorf("output contains %q:\n%s", n, out)
				}
			}
		})
	}
}
