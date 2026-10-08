package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestBranchingExampleRunsKeyless runs the example and checks the tree and
// the requests its README describes.
func TestBranchingExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	s := out.String()
	for _, want := range []string{
		"user      [The conversation was rewound. A summary of the abandoned branch follows.]",
		"leaves [e07 e10], head e10",
		"branch to e07: e02 → e03 → e04 → e05 → e06 → e07",
		"branch to e10: e02 → e03 → e08 → e09 → e10",
		`refused, as it should be: agentkit: the session store's head is "e07" but this Resume was folded from "e10"`,
		"e12 assistant Run pg_partman's maintenance from pg_cron every night.   ← head",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q\n%s", want, s)
		}
	}
	// On the new branch the model must not see the abandoned turns.
	newBranch := s[strings.Index(s, "what the model was sent on the new branch"):strings.Index(s, "── 3.")]
	if strings.Contains(newBranch, "    user      Show me the schema.") {
		t.Errorf("the new branch's request carried an abandoned turn:\n%s", newBranch)
	}
}
