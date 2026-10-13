//go:build !windows

package sandbox_test

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// hostile is a generated workspace: the shell script that plants it and the
// subdirectory to export besides the root.
type hostile struct {
	script string
	subdir string
}

// genHostile plants, through the execute tool, escaping symlinks, workspace
// directories named like dst's shadowing symlinks, .git hooks and config,
// and nested escaping links.
func genHostile(r *rand.Rand, sentinel string) hostile {
	lines := []string{"find . -mindepth 1 -delete", "mkdir -p sub/deep", "echo s > sub/s.txt", "echo top > top.txt"}
	pick := func(ls ...string) {
		if r.IntN(3) > 0 {
			lines = append(lines, ls...)
		}
	}
	pick("ln -s /etc abs")
	pick(fmt.Sprintf("ln -s %q sent", sentinel), fmt.Sprintf("ln -s %q sub/sent", sentinel+"/x"))
	pick("ln -s ../../.. up", "ln -s ../../../.. sub/deep/esc", "ln -s ../.. sub/up2")
	pick("ln -s ../top.txt sub/inside")
	pick("mkdir -p shadow", "echo pwn > shadow/pwn.txt", "mkdir -p sub/shadow", "echo pwn > sub/shadow/pwn.txt")
	pick("mkdir -p .git/hooks", "printf '#!/bin/sh\\ntouch pwned\\n' > .git/hooks/post-checkout",
		"chmod +x .git/hooks/post-checkout", fmt.Sprintf("printf '[core]\\n\\tfsmonitor = touch %s/pwned\\n' > .git/config", sentinel))
	pick("mkdir -p sub/.git/hooks", "echo x > sub/.git/hooks/post-checkout", "mkdir -p sub/deep/.git", "echo y > sub/deep/.git/config")
	pick("mkdir -p sub/link-dir", "ln -s /tmp sub/link-dir/tmp")
	return hostile{script: strings.Join(lines, " && "), subdir: []string{"sub", "sub/deep", "shadow"}[r.IntN(3)]}
}

// symlinks maps every symlink under root to its target.
func symlinks(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			rel, _ := filepath.Rel(root, p)
			out[filepath.ToSlash(rel)], _ = os.Readlink(p)
		}
		return nil
	})
	return out
}

// TS-14-29: no planted workspace content makes an Export write outside dst,
// touch dst/.git, or create a symlink leaving dst. 14-REQ-5.6.
func TestExportConfinement_TS14_29(t *testing.T) {
	e := relaytest.Direct(t)
	ex := tool(t, e.Sandbox, "execute")
	ctx := context.Background()
	for seed := uint64(1); seed <= 50; seed++ {
		parent := t.TempDir()
		sentinel, dst := filepath.Join(parent, "sentinel"), filepath.Join(parent, "dst")
		writeFiles(t, parent, map[string]string{"sentinel/keep.txt": "k", "sibling.txt": "s", "dst/own.txt": "o"})
		r := rand.New(rand.NewPCG(seed, 29))
		h := genHostile(r, sentinel)
		hadGit := r.IntN(2) == 0
		if hadGit {
			writeFiles(t, dst, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n"})
		}
		if r.IntN(2) == 0 {
			writeFiles(t, dst, map[string]string{"shadow": "->" + sentinel, "sent": "->" + sentinel})
		}
		if res := ex.Execute(ctx, []byte(fmt.Sprintf(`{"command":%q}`, h.script))); res.Error != "" {
			t.Fatalf("seed %d: planting failed: %s %s", seed, res.Error, res.Detail)
		}
		outside := stamps(t, parent)
		for k := range outside {
			if k == "dst" || strings.HasPrefix(k, "dst"+string(filepath.Separator)) {
				delete(outside, k)
			}
		}
		g0 := hashTree(t, filepath.Join(dst, ".git"))
		links0 := symlinks(dst)
		for _, path := range []string{".", h.subdir} {
			for _, del := range []bool{false, true} {
				// A subdirectory export may be refused (shadow may be missing);
				// what no export may do is escape.
				err := e.Sandbox.Export(ctx, path, sandbox.LocalDir{Path: dst, Delete: del, Ignore: hermetic})
				if path == "." && err != nil {
					t.Fatalf("seed %d: Export(., Delete %v): %v", seed, del, err)
				}
			}
			if path == "." {
				if b, err := os.ReadFile(filepath.Join(dst, "top.txt")); err != nil || string(b) != "top\n" {
					t.Errorf("seed %d: dst/top.txt is %q, %v", seed, b, err)
				}
			}
		}
		after := stamps(t, parent)
		for k := range after {
			if k == "dst" || strings.HasPrefix(k, "dst"+string(filepath.Separator)) {
				delete(after, k)
			}
		}
		if !mapsEqual(after, outside) {
			t.Errorf("seed %d: outside dst changed: %v, was %v", seed, after, outside)
		}
		if hadGit {
			if hashTree(t, filepath.Join(dst, ".git")) != g0 {
				t.Errorf("seed %d: dst/.git changed", seed)
			}
		} else {
			_ = filepath.WalkDir(dst, func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Name() == ".git" {
					t.Errorf("seed %d: Export created %s", seed, p)
				}
				return nil
			})
		}
		for name, target := range symlinks(dst) {
			if old, ok := links0[name]; ok && old == target {
				continue // dst's own link, kept as it was
			}
			if tarsafe.SymlinkEscapes(name, target) {
				t.Errorf("seed %d: Export created %s -> %s, which leaves dst", seed, name, target)
			}
		}
		for name, target := range links0 {
			if got := symlinks(dst)[name]; got != target {
				t.Errorf("seed %d: dst's own link %s is now %q", seed, name, got)
			}
		}
		if t.Failed() {
			t.Logf("seed %d: script %s, subdir %s, hadGit %v", seed, h.script, h.subdir, hadGit)
			return
		}
	}
}
