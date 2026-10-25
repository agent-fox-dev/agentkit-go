package main

import (
	"context"
	"log"
	"runtime"
	"strings"
	"testing"
)

// demo runs the demo in process with the temporary directory pointed at one
// of the test's own, which is returned too, so what the demo leaves behind can
// be counted and none of it reaches the real one. zoekt logs through the
// standard logger, which the demo points nowhere for the run: a line that
// reaches the test's buffer is one it let through.
func demo(t *testing.T) (out, tmp string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("zoekt cannot run on windows: the demo prints that and stops")
	}
	tmp = t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var logs strings.Builder
	was := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(was)
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if logs.Len() > 0 {
		t.Errorf("the demo let the standard logger write:\n%s", logs.String())
	}
	return b.String(), tmp
}

// part is the text of out from the line that starts with start up to the next
// heading, so a check on one step cannot be met by the output of another.
func part(t *testing.T, out, start string) string {
	t.Helper()
	i := strings.Index(out, "\n"+start)
	if i < 0 {
		t.Fatalf("the output has no %q", start)
	}
	rest := out[i+1:]
	if j := strings.Index(rest[len(start):], "\n#"); j >= 0 {
		return rest[:len(start)+j]
	}
	return rest
}
