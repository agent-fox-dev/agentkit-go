package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The sample holds one file of each kind the index cannot search, and the
// rest it can: this is what the demo's counts stand on.
func TestTheSampleHoldsOneFileOfEachSkipReason(t *testing.T) {
	dir := t.TempDir()
	if err := writeSample(dir); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if n := len(read("data/flag.txt")); n >= 3 {
		t.Errorf("data/flag.txt is %d bytes, want fewer than 3 (too small)", n)
	}
	if n := len(read("data/orders.csv")); n <= 1<<20 {
		t.Errorf("data/orders.csv is %d bytes, want over 1 MiB", n)
	}
	if !strings.Contains(string(read("assets/logo.png")), "\x00") {
		t.Error("assets/logo.png holds no NUL byte (binary)")
	}
	bundle, trigrams := read("web/app.min.js"), map[string]bool{}
	for i := 0; i+3 <= len(bundle); i++ {
		trigrams[string(bundle[i:i+3])] = true
	}
	if len(trigrams) <= 20000 {
		t.Errorf("web/app.min.js has %d distinct trigrams, want over zoekt's 20000", len(trigrams))
	}
	if gen, _ := filepath.Glob(filepath.Join(dir, "gen", "gen_*.go")); len(gen) != genFiles {
		t.Errorf("%d generated files, want %d", len(gen), genFiles)
	}
}
