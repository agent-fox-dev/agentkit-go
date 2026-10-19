package guard

import (
	"fmt"
	"slices"
	"strings"
)

// Command is one simple command of a shell line, as Commands reads it.
type Command struct {
	// Text is the command as written, trimmed: its words and redirections,
	// without the operator that ends it or a heredoc's body.
	Text string
	// Assign are the names of the NAME=value assignments in front of the
	// program, in order. A command that is only assignments has an empty Argv.
	Assign []string
	// Argv is the program and its arguments with quotes and escapes removed.
	// Expansions are left as written ($HOME, ${x}, $(date), `date`), so a word
	// the shell would compute is never spelled like a plain program name.
	Argv []string
	// Redirects are the command's redirections, in order.
	Redirects []Redirect
}

// Redirect is one redirection of a Command.
type Redirect struct {
	// FD is the file descriptor written in front of the operator ("2" in
	// 2>&1), or "" when there is none.
	FD string
	// Op is the operator: <, >, >>, >|, <>, <&, >&, &>, &>>, <<, <<- or <<<.
	Op string
	// Target is the word after the operator, unquoted: a file, a descriptor,
	// a here-string or, for << and <<-, the heredoc's delimiter.
	Target string
}

// SyntaxError is Commands' refusal of a line it cannot read with certainty:
// one the shell would reject (an unterminated quote, an unmatched
// parenthesis), or a construct Commands does not follow (a case statement,
// a heredoc with no terminating line). A guard refuses such a line: the
// commands Commands would return for it are not the ones the shell runs.
type SyntaxError struct {
	// Offset is the byte offset in the line where reading stopped.
	Offset int
	// Reason says what was found there.
	Reason string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("shell syntax: %s (at byte %d)", e.Reason, e.Offset)
}

// maxNesting bounds how deeply subshells and substitutions may nest, so a
// hostile line cannot exhaust the stack.
const maxNesting = 64

// Commands splits a POSIX-sh / bash command line into every simple command it
// runs: the ones separated by `;`, `&`, `&&`, `||`, `|`, `|&` and newlines, the
// ones inside subshells, and the ones inside command substitutions ($(...) and
// backticks), process substitutions (<(...), >(...)), parameter expansions
// (${x:-$(...)}), arithmetic ($((...))) and the bodies of unquoted heredocs.
// Commands are returned in the order they start in the line; a substituted
// command follows the command it appears in.
//
// What the shell treats as data stays data: quoted text, comments, and the
// body of a heredoc (apart from the substitutions an unquoted one expands).
//
// It reads the grammar, it does not run it: expansions are not expanded,
// aliases and functions are not resolved, and the keywords of compound
// commands (if, for, while, {, [[, !, time) are words like any other, so a
// guard that matches Argv[0] against an allowlist refuses them unless they
// are listed. A case statement, whose patterns end in an unbalanced `)`, is
// refused with a *SyntaxError rather than misread.
//
// It returns a *SyntaxError for a line it cannot read with certainty; see
// SyntaxError.
func Commands(line string) ([]Command, error) {
	p := &parser{src: line}
	if err := p.list(0, 0); err != nil {
		return nil, err
	}
	return p.out, nil
}

// parser reads one line. Commands found anywhere in it are appended to out.
type parser struct {
	src string
	i   int
	out []Command
	// pending are the heredocs opened on the current line, whose bodies start
	// after its newline.
	pending []heredoc
}

type heredoc struct {
	delim  string
	strip  bool // <<-: leading tabs are ignored on the body's lines
	quoted bool // a quoted delimiter: the body expands nothing
	depth  int  // the nesting depth of the list whose newline starts the body
}

// quoting is where a word part is read: unquoted, inside double quotes, or in
// an unquoted heredoc body (double quotes without the closing quote).
type quoting int

const (
	unquoted quoting = iota
	inDouble
	inHeredoc
)

func (p *parser) errorf(format string, args ...any) error {
	return &SyntaxError{Offset: min(p.i, len(p.src)), Reason: fmt.Sprintf(format, args...)}
}

// skipEscape steps over a backslash and the byte it escapes, if any.
func (p *parser) skipEscape() { p.i = min(p.i+2, len(p.src)) }

func (p *parser) eof() bool { return p.i >= len(p.src) }

// at returns the byte k positions ahead, or 0 past the end.
func (p *parser) at(k int) byte {
	if p.i+k < len(p.src) {
		return p.src[p.i+k]
	}
	return 0
}

// skipBlanks skips spaces, tabs and line continuations.
func (p *parser) skipBlanks() {
	for !p.eof() {
		switch {
		case p.src[p.i] == ' ' || p.src[p.i] == '\t':
			p.i++
		case p.src[p.i] == '\\' && p.at(1) == '\n':
			p.i += 2
		default:
			return
		}
	}
}

func (p *parser) skipComment() {
	for !p.eof() && p.src[p.i] != '\n' {
		p.i++
	}
}

// list reads commands until the end of the input or, when closer is ')', the
// unquoted ')' that ends a subshell or substitution.
func (p *parser) list(depth int, closer byte) error {
	if depth > maxNesting {
		return p.errorf("nested more than %d levels deep", maxNesting)
	}
	for {
		p.skipBlanks()
		if p.eof() {
			if closer != 0 {
				return p.errorf("unterminated %q", "(")
			}
			if len(p.pending) > 0 {
				return p.errorf("heredoc <<%s has no terminating line", p.pending[0].delim)
			}
			return nil
		}
		switch c := p.src[p.i]; {
		case c == ')':
			if closer != ')' {
				return p.errorf("unmatched %q", ")")
			}
			p.i++
			return nil
		case c == '\n':
			p.i++
			if err := p.heredocBodies(depth); err != nil {
				return err
			}
		case c == ';' || c == '|' || (c == '&' && p.at(1) != '>'):
			p.i++ // between commands; an empty command is skipped
		case c == '#':
			p.skipComment()
		default:
			if err := p.command(depth); err != nil {
				return err
			}
		}
	}
}

// command reads one simple command, a subshell or an arithmetic command.
func (p *parser) command(depth int) error {
	start := p.i
	if p.src[p.i] == '(' {
		if p.at(1) == '(' {
			ok, err := p.arithmetic(depth, start+2)
			if err != nil || ok {
				return err
			}
		}
		p.i = start + 1
		if err := p.list(depth+1, ')'); err != nil {
			return err
		}
		// Redirections after the subshell are read as a command of their own,
		// with no program.
		return p.simple(depth, p.i)
	}
	return p.simple(depth, start)
}

// simple reads the words and redirections of one simple command starting at
// start, up to the operator or newline that ends it.
func (p *parser) simple(depth, start int) error {
	idx := len(p.out)
	p.out = append(p.out, Command{}) // the command precedes the ones substituted into it
	var cmd Command
	for {
		p.skipBlanks()
		if p.eof() {
			break
		}
		c := p.src[p.i]
		if c == '\n' || c == ';' || c == '|' || c == ')' || c == '#' || (c == '&' && p.at(1) != '>') {
			break
		}
		if c == '(' {
			return p.errorf("%q in the middle of a command (a function definition or an array) is not supported", "(")
		}
		if (c == '<' || c == '>') && p.at(1) != '(' || c == '&' {
			if err := p.redirect(depth, &cmd, ""); err != nil {
				return err
			}
			continue
		}
		raw, text, err := p.word(depth)
		if err != nil {
			return err
		}
		if (p.at(0) == '<' || p.at(0) == '>') && p.at(1) != '(' && isDigits(raw) {
			if err := p.redirect(depth, &cmd, raw); err != nil {
				return err
			}
			continue
		}
		if len(cmd.Argv) == 0 {
			if name, ok := assignment(raw); ok {
				cmd.Assign = append(cmd.Assign, name)
				continue
			}
		}
		cmd.Argv = append(cmd.Argv, text)
	}
	cmd.Text = strings.TrimSpace(p.src[start:p.i])
	if len(cmd.Argv) > 0 && cmd.Argv[0] == "case" {
		return &SyntaxError{Offset: start, Reason: "a case statement is not supported"}
	}
	if len(cmd.Argv) == 0 && len(cmd.Assign) == 0 && len(cmd.Redirects) == 0 {
		p.out = slices.Delete(p.out, idx, idx+1)
		return nil
	}
	p.out[idx] = cmd
	return nil
}

// redirect reads a redirection operator and its target.
func (p *parser) redirect(depth int, cmd *Command, fd string) error {
	var op string
	for _, o := range []string{"&>>", "<<<", "<<-", "&>", "<<", "<>", "<&", ">>", ">|", ">&", "<", ">"} {
		if strings.HasPrefix(p.src[p.i:], o) {
			op = o
			break
		}
	}
	if op == "" {
		return p.errorf("unexpected %q", p.src[p.i:p.i+1])
	}
	p.i += len(op)
	p.skipBlanks()
	procSub := (p.at(0) == '<' || p.at(0) == '>') && p.at(1) == '('
	if p.eof() || !procSub && strings.IndexByte(" \t\n;&|()<>", p.src[p.i]) >= 0 {
		return p.errorf("redirection %s has no target", op)
	}
	raw, text, err := p.word(depth)
	if err != nil {
		return err
	}
	cmd.Redirects = append(cmd.Redirects, Redirect{FD: fd, Op: op, Target: text})
	if op == "<<" || op == "<<-" {
		p.pending = append(p.pending, heredoc{
			delim:  text,
			strip:  op == "<<-",
			quoted: strings.ContainsAny(raw, `'"\`),
			depth:  depth,
		})
	}
	return nil
}

// heredocBodies consumes the bodies of the heredocs opened on the line that
// just ended, finding the commands an unquoted one substitutes.
func (p *parser) heredocBodies(depth int) error {
	pending := p.pending
	p.pending = nil
	for _, h := range pending {
		if h.depth != depth {
			return p.errorf("heredoc <<%s whose line continues inside a nested command is not supported", h.delim)
		}
		bodyStart := p.i
		for {
			if p.eof() {
				return p.errorf("heredoc <<%s has no terminating line", h.delim)
			}
			lineStart := p.i
			line, rest, found := strings.Cut(p.src[p.i:], "\n")
			if found {
				p.i = len(p.src) - len(rest)
			} else {
				p.i = len(p.src)
			}
			if h.strip {
				line = strings.TrimLeft(line, "\t")
			}
			if line != h.delim {
				continue
			}
			if !h.quoted {
				sub := &parser{src: p.src[bodyStart:lineStart]}
				if err := sub.expansions(depth + 1); err != nil {
					return shift(err, bodyStart)
				}
				p.out = append(p.out, sub.out...)
			}
			break
		}
	}
	return nil
}

// expansions scans an unquoted heredoc body for the substitutions the shell
// expands in it.
func (p *parser) expansions(depth int) error {
	for !p.eof() {
		switch p.src[p.i] {
		case '\\':
			p.skipEscape()
		case '$':
			if _, err := p.dollar(depth, inHeredoc); err != nil {
				return err
			}
		case '`':
			if _, err := p.backtick(depth); err != nil {
				return err
			}
		default:
			p.i++
		}
	}
	return nil
}

// word reads one unquoted word, up to a blank or an operator. raw is the word
// as written; text has its quotes and escapes removed and its expansions left
// as written.
func (p *parser) word(depth int) (raw, text string, err error) {
	start := p.i
	var b strings.Builder
	for !p.eof() {
		c := p.src[p.i]
		if (c == '<' || c == '>') && p.at(1) == '(' {
			s, found := p.i, len(p.out)
			p.i += 2
			if err := p.list(depth+1, ')'); err != nil {
				return "", "", err
			}
			if len(p.out) == found {
				// bash drops an empty one and splits the word there.
				return "", "", &SyntaxError{Offset: s, Reason: "an empty process substitution is not supported"}
			}
			b.WriteString(p.src[s:p.i])
			continue
		}
		if strings.IndexByte(" \t\n;&|()<>", c) >= 0 {
			break
		}
		switch c {
		case '\\':
			switch {
			case p.at(1) == '\n':
				p.i += 2 // a line continuation joins the word
			case p.i+1 < len(p.src):
				b.WriteByte(p.src[p.i+1])
				p.i += 2
			default:
				b.WriteByte(c)
				p.i++
			}
		case '\'':
			end := strings.IndexByte(p.src[p.i+1:], '\'')
			if end < 0 {
				return "", "", p.errorf("unterminated %q", "'")
			}
			b.WriteString(p.src[p.i+1 : p.i+1+end])
			p.i += end + 2
		case '"':
			t, err := p.dquote(depth)
			if err != nil {
				return "", "", err
			}
			b.WriteString(t)
		case '$':
			t, err := p.dollar(depth, unquoted)
			if err != nil {
				return "", "", err
			}
			b.WriteString(t)
		case '`':
			t, err := p.backtick(depth)
			if err != nil {
				return "", "", err
			}
			b.WriteString(t)
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	return p.src[start:p.i], b.String(), nil
}

// dquote reads a double-quoted part starting at its opening quote and returns
// its text.
func (p *parser) dquote(depth int) (string, error) {
	open := p.i
	p.i++
	var b strings.Builder
	for {
		if p.eof() {
			p.i = open
			return "", p.errorf("unterminated %q", `"`)
		}
		switch c := p.src[p.i]; c {
		case '"':
			p.i++
			return b.String(), nil
		case '\\':
			switch n := p.at(1); {
			case n == '\n':
				p.i += 2
			case n == '$' || n == '`' || n == '"' || n == '\\':
				b.WriteByte(n)
				p.i += 2
			default:
				b.WriteByte(c)
				p.i++
			}
		case '$':
			t, err := p.dollar(depth, inDouble)
			if err != nil {
				return "", err
			}
			b.WriteString(t)
		case '`':
			t, err := p.backtick(depth)
			if err != nil {
				return "", err
			}
			b.WriteString(t)
		default:
			b.WriteByte(c)
			p.i++
		}
	}
}

// dollar reads what starts at a `$` and returns it as written: a command
// substitution, arithmetic, a parameter expansion, a variable, ANSI-C or
// locale quoting, or a literal `$`.
func (p *parser) dollar(depth int, q quoting) (string, error) {
	start := p.i
	switch n := p.at(1); {
	case n == '(' && p.at(2) == '(':
		ok, err := p.arithmetic(depth, start+3)
		if err != nil {
			return "", err
		}
		if !ok { // $( (subshell) ... ): a command substitution after all
			p.i = start + 2
			if err := p.list(depth+1, ')'); err != nil {
				return "", err
			}
		}
	case n == '(':
		p.i += 2
		if err := p.list(depth+1, ')'); err != nil {
			return "", err
		}
	case n == '{':
		p.i += 2
		if err := p.brace(depth, q); err != nil {
			return "", err
		}
	case n == '\'' && q == unquoted:
		// $'...': backslash escapes, including \', are expanded.
		j := p.i + 2
		for ; j < len(p.src) && p.src[j] != '\''; j++ {
			if p.src[j] == '\\' {
				j++
			}
		}
		if j >= len(p.src) {
			return "", p.errorf("unterminated %q", "$'")
		}
		p.i = j + 1
	case n == '"' && q == unquoted:
		p.i++
		if _, err := p.dquote(depth); err != nil {
			return "", err
		}
	case isNameByte(n):
		p.i++
		for !p.eof() && (isNameByte(p.src[p.i]) || '0' <= p.src[p.i] && p.src[p.i] <= '9') {
			p.i++
		}
	case n != 0 && strings.IndexByte("0123456789@*#?$!-", n) >= 0:
		p.i += 2
	default:
		p.i++
	}
	return p.src[start:p.i], nil
}

// brace reads a parameter expansion's body, after its `${`, through the `}`
// that closes it. The word in ${x:-word} is expanded, so the substitutions in
// it are commands. Single quotes quote in it only outside double quotes.
func (p *parser) brace(depth int, q quoting) error {
	if depth > maxNesting {
		return p.errorf("nested more than %d levels deep", maxNesting)
	}
	for {
		if p.eof() {
			return p.errorf("unterminated %q", "${")
		}
		switch p.src[p.i] {
		case '}':
			p.i++
			return nil
		case '\\':
			p.skipEscape()
		case '\'':
			if q != unquoted {
				p.i++
				continue
			}
			end := strings.IndexByte(p.src[p.i+1:], '\'')
			if end < 0 {
				return p.errorf("unterminated %q", "'")
			}
			p.i += end + 2
		case '"':
			if _, err := p.dquote(depth + 1); err != nil {
				return err
			}
		case '$':
			if _, err := p.dollar(depth+1, q); err != nil {
				return err
			}
		case '`':
			if _, err := p.backtick(depth + 1); err != nil {
				return err
			}
		case '<', '>':
			if p.at(1) != '(' {
				p.i++
				continue
			}
			// ${x:-<(cmd)} runs cmd. Read as one in double quotes too: a
			// command reported that the shell does not run is the safe error.
			p.i += 2
			if err := p.list(depth+1, ')'); err != nil {
				return err
			}
		default:
			p.i++
		}
	}
}

// arithmetic reads an arithmetic expression whose body starts at from, after
// `((` or `$((`, through the `))` that closes it. ok is false, with nothing
// consumed, when the parentheses close some other way: the text was a
// subshell inside a subshell or a substitution, as the shell reads it too.
// The substitutions in the expression are commands.
func (p *parser) arithmetic(depth, from int) (ok bool, err error) {
	start, found, pending := p.i, len(p.out), len(p.pending)
	p.i = from
	open := 0
	for {
		if p.eof() {
			return false, p.errorf("unterminated %q", "((")
		}
		switch p.src[p.i] {
		case '(':
			open++
			p.i++
		case ')':
			if open > 0 {
				open--
				p.i++
				continue
			}
			if p.at(1) == ')' {
				p.i += 2
				return true, nil
			}
			p.i, p.out, p.pending = start, p.out[:found], p.pending[:pending]
			return false, nil
		case '\\':
			p.skipEscape()
		case '\'':
			end := strings.IndexByte(p.src[p.i+1:], '\'')
			if end < 0 {
				return false, p.errorf("unterminated %q", "'")
			}
			p.i += end + 2
		case '"':
			if _, err := p.dquote(depth + 1); err != nil {
				return false, err
			}
		case '$':
			if _, err := p.dollar(depth+1, inDouble); err != nil {
				return false, err
			}
		case '`':
			if _, err := p.backtick(depth + 1); err != nil {
				return false, err
			}
		default:
			p.i++
		}
	}
}

// backtick reads a backtick substitution starting at its opening backtick and
// returns it as written. Inside it a backslash escapes only $, ` and \; the
// unescaped text is read as a line of its own.
func (p *parser) backtick(depth int) (string, error) {
	start := p.i
	var inner strings.Builder
	j := p.i + 1
	for ; j < len(p.src) && p.src[j] != '`'; j++ {
		if p.src[j] == '\\' && j+1 < len(p.src) && strings.IndexByte("$`\\", p.src[j+1]) >= 0 {
			j++
		}
		inner.WriteByte(p.src[j])
	}
	if j >= len(p.src) {
		return "", p.errorf("unterminated %q", "`")
	}
	if depth >= maxNesting {
		return "", p.errorf("nested more than %d levels deep", maxNesting)
	}
	sub := &parser{src: inner.String()}
	if err := sub.list(depth+1, 0); err != nil {
		return "", shift(err, start+1)
	}
	p.out = append(p.out, sub.out...)
	p.i = j + 1
	return p.src[start:p.i], nil
}

// shift moves a sub-parser's syntax error to the offset of the text it read.
func shift(err error, by int) error {
	if se, ok := err.(*SyntaxError); ok {
		return &SyntaxError{Offset: se.Offset + by, Reason: se.Reason}
	}
	return err
}

// assignment reports whether raw, a word as written, is a NAME=value
// assignment, and returns NAME. NAME must be a shell identifier spelled
// without quotes: bash runs `x-y=1 ls` and `'X=1' ls` as the commands `x-y=1`
// and `X=1`.
func assignment(raw string) (string, bool) {
	i := strings.IndexByte(raw, '=')
	if i <= 0 || !isIdentifier(raw[:i]) {
		return "", false
	}
	return raw[:i], true
}

func isNameByte(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
