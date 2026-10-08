package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestCompactionExampleRunsKeyless runs the whole example against its
// scripted providers and checks the behaviours its README describes.
func TestCompactionExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"summary was truncated at the output limit", // the failure taxonomy
		"[checkpoint] summarized messages",          // a checkpoint was written
		"turn 6: history 12 msgs → sent  5 msgs",    // the compacted view
		"split turn",               // the split-turn summarizer
		"checkpoint present: true", // folded back from the log
		"next turn sent 7 of 14 messages  ← starts with the summary",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q\n%s", want, out.String())
		}
	}
}
