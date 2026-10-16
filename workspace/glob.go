package workspace

import "github.com/bmatcuk/doublestar/v4"

// globMatch is doublestar.Match with a malformed pattern matching nothing.
// tools keeps its own copy beside MatchGlob: the packages share no helper, so
// workspace imports nothing of the module.
func globMatch(pattern, name string) bool {
	ok, err := doublestar.Match(pattern, name)
	return ok && err == nil
}
