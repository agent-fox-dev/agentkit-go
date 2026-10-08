package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestImagesExampleRunsKeyless runs every keyless section and checks the
// behaviours its README describes.
func TestImagesExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, false); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"Sniff(screenshot.txt)  → image/png",
		"Normalize              → image/png 2000×1125, changed=true",
		"Normalize(again)       → changed=false",
		"sent block: image image/png 2000×1125",
		"in history (read_file): image image/png 2000×1125",
		"the tool returned: image image/png 3200×1800",
		"in history (take_snapshot): image image/png 2000×1125",
		"repairs: 1 images replaced",
		"skipped: pass --real",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q\n%s", want, out.String())
		}
	}
}
