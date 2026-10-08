package tools

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// MatchGlob matches a path against a glob pattern.
//
// Matching is github.com/bmatcuk/doublestar/v4: `**` crosses separators (and
// matches zero segments), `*` and `?` stay within one, `[abc]` / `[!abc]`
// classes, `{a,b}` alternation. filepath.Match alone cannot cross `/` with
// `**`, and a find tool that silently returned node_modules would be worse
// than no find tool. On top of it, two rules of AgentKit's own:
//
//   - smart-case: an all-lowercase pattern matches case-insensitively;
//   - a bare alternative with no separator also matches by basename, which is
//     what a user typing `*.go` means.
func MatchGlob(pattern, path string) bool {
	path = filepath.ToSlash(path)
	if pattern == strings.ToLower(pattern) {
		path = strings.ToLower(path)
	}
	base := path[strings.LastIndexByte(path, '/')+1:]
	for _, p := range ExpandBraces(pattern) {
		p = literalBraces.Replace(p) // what expansion left is unbalanced: literal
		if globMatch(p, path) || !strings.Contains(p, "/") && globMatch(p, base) {
			return true
		}
	}
	return false
}

var literalBraces = strings.NewReplacer("{", `\{`, "}", `\}`)

// globMatch is doublestar.Match with a malformed pattern matching nothing.
func globMatch(pattern, name string) bool {
	ok, err := doublestar.Match(pattern, name)
	return ok && err == nil
}

// ExpandBraces expands {a,b} alternations, including nested ones.
func ExpandBraces(p string) []string {
	start := strings.IndexByte(p, '{')
	if start < 0 {
		return []string{p}
	}
	depth, end := 0, -1
	for i := start; i < len(p); i++ {
		switch p[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return []string{p} // unbalanced: treat literally
	}

	// Split the alternation on top-level commas only.
	body := p[start+1 : end]
	var alts []string
	d, last := 0, 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{':
			d++
		case '}':
			d--
		case ',':
			if d == 0 {
				alts = append(alts, body[last:i])
				last = i + 1
			}
		}
	}
	alts = append(alts, body[last:])

	var out []string
	for _, a := range alts {
		out = append(out, ExpandBraces(p[:start]+a+p[end+1:])...)
	}
	return out
}
