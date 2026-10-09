package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TS-08-49 (smoke, 08-PATH-5): the example runs offline against
// provider/faux and exits zero, both as a program and in process.
func TestExampleRunsOffline_TS08_49(t *testing.T) {
	out, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Code mode example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Go files: main.go, util.go", "CHANGELOG.md: read_failed",
		`Return value: {"apples": 12, "pears": 0}`, "called inventory__stock"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
}
