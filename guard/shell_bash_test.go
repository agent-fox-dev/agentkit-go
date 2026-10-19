package guard

import (
	"context"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCommandsAgainstBash checks Commands against the shell it reads: random
// lines built from shell fragments are run by bash with a PATH holding only
// marker programs that log their own name, and every marker bash ran must be
// the program of a command Commands reported. A line Commands refuses is not
// run. A line with a computed program (a word holding $ or a backtick) or a
// keyword in program position is skipped: a guard refuses it by name, whatever
// it runs.
//
// It needs bash and takes a while, so it runs only with AGENTKIT_BASH_DIFF=1;
// AGENTKIT_BASH_DIFF_LINES sets the number of lines (default 20000).
func TestCommandsAgainstBash(t *testing.T) {
	if os.Getenv("AGENTKIT_BASH_DIFF") != "1" {
		t.Skip("set AGENTKIT_BASH_DIFF=1 to run Commands against bash")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	lines := 20000
	if s := os.Getenv("AGENTKIT_BASH_DIFF_LINES"); s != "" {
		if lines, err = strconv.Atoi(s); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		name := "m" + strconv.Itoa(i)
		script := "#!/bin/sh\necho " + name + " >> \"$MARKER_LOG\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	frags := []string{
		"m0 <<EOF\n$(m1)\nEOF\n", "m2 <<'X'\nm3\nX\n", "$(m4; m5)", "\"$(m1)\"", "`m2`", "m3 <(m4)", "${y:-$(m5)}",
		"${z:-m1 <(m2) >(m3)}", "\"${z:-<(m4)}\"", " # m1\n", "((a<<b))\n",
		"m0", "m1", "m2", "m3 a", "m4", "m5", " ", " ", ";", "|", "&&", "||", "&", "$(", ")", "(", "`", "\"", "'",
		"<<EOF\n", "\nEOF\n", "<<'EOF'\n", "<<-EOF\n", "\tEOF\n", "${x:-", "}", "((", "))", "$((", "#", "\n", "<(", ">(",
		"\\", "\\\n", "x=", "$x", "1<<2", "case", " in ", "esac", ";;", "2>&1", ">", "<", "{", "}", "!", "if ", "then ",
		"fi", "$'", "eval ",
	}
	refusable := map[string]bool{"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true,
		"while": true, "until": true, "do": true, "done": true, "!": true, "{": true, "}": true, "[[": true,
		"eval": true, "exec": true, "time": true, "coproc": true, "function": true, "select": true, "in": true,
		"esac": true, "case": true}
	rng := rand.New(rand.NewPCG(1, uint64(time.Now().UnixNano())))
	read, missed := 0, 0
	for k := range lines {
		var b strings.Builder
		for range 2 + rng.IntN(10) {
			b.WriteString(frags[rng.IntN(len(frags))])
		}
		line := b.String()
		cmds, err := Commands(line)
		if err != nil {
			continue
		}
		read++
		reported, skip := map[string]bool{}, false
		for _, c := range cmds {
			if len(c.Argv) > 0 {
				reported[c.Argv[0]] = true
				skip = skip || strings.ContainsAny(c.Argv[0], "$`") || refusable[c.Argv[0]]
			}
		}
		if skip {
			continue
		}
		// A log per line: bash does not wait for a background job, which may
		// write after the next line starts.
		log := filepath.Join(dir, "log"+strconv.Itoa(k))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, bash, "-c", line)
		cmd.Env = []string{"PATH=" + bin, "MARKER_LOG=" + log}
		cmd.Dir = dir
		_ = cmd.Run()
		cancel()
		ran, _ := os.ReadFile(log)
		for _, m := range strings.Fields(string(ran)) {
			if !reported[m] {
				missed++
				if missed <= 10 {
					t.Errorf("bash ran %s, which Commands did not report: %q -> %+v", m, line, cmds)
				}
			}
		}
	}
	t.Logf("%d lines, %d read by Commands, %d programs missed", lines, read, missed)
}
