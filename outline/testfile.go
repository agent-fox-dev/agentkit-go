package outline

import (
	"path"
	"strings"
)

// testDirs are the directory names whose whole subtree is tests, test data
// or test support code.
var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "spec": true,
	"testdata": true, "testutil": true, "testutils": true, "testhelpers": true,
}

// IsTestFile reports whether rel, a slash-separated path relative to a
// workspace root, is a test, test data or test support code, by the naming
// and directory conventions of the languages in the extension table
// (LangForExt). It is the one classifier the tools rank by, so a consumer
// that ranks, filters or splits files the same way can agree with them.
//
// Any file under a directory named test, tests, __tests__, spec, testdata,
// testutil, testutils or testhelpers counts. Otherwise a source file (one
// whose extension is in the table) counts when its name, without the
// extension:
//
//   - ends in _test or starts with test_ (Go, Python, Rust, C, Ruby, ...);
//   - ends in _spec (Ruby) or in .test or .spec (JavaScript, TypeScript);
//   - ends in Test or Tests after at least one character (Java, Kotlin, C#,
//     Ruby), or in IT (Java integration tests);
//   - is test support: conftest.py, setupTests, jest.setup or vitest.setup.
//
// The rule is a convention, not a parse: it reads no file.
func IsTestFile(rel string) bool {
	dir, base := path.Split(rel)
	for _, seg := range strings.Split(strings.TrimSuffix(dir, "/"), "/") {
		if testDirs[seg] {
			return true
		}
	}
	ext := path.Ext(base)
	if langForExt(ext) == "" {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	switch {
	case base == "conftest.py", stem == "setupTests", stem == "jest.setup", stem == "vitest.setup":
		return true
	case strings.HasSuffix(stem, "_test"), strings.HasPrefix(stem, "test_"), strings.HasSuffix(stem, "_spec"),
		strings.HasSuffix(stem, ".test"), strings.HasSuffix(stem, ".spec"):
		return true
	}
	switch strings.ToLower(ext) {
	case ".java":
		if len(stem) > len("IT") && strings.HasSuffix(stem, "IT") {
			return true
		}
		fallthrough
	case ".kt", ".kts", ".cs", ".rb":
		return len(stem) > len("Test") && strings.HasSuffix(stem, "Test") ||
			len(stem) > len("Tests") && strings.HasSuffix(stem, "Tests")
	}
	return false
}
