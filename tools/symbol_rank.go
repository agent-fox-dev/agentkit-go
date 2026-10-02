package tools

import (
	"path/filepath"
	"sort"
	"strings"
)

// isTestFile classifies a file as a test file by the fixed rule in 02-REQ-4.2.
//
// Go: _test.go
// Python: test_*.py and *_test.py
// JS/TS: *.test.* and *.spec.*
// Java, Kotlin, C#, Ruby: base name (without extension) ends in Test, Tests or _spec
// Any file under a directory named test, tests, __tests__ or spec
func isTestFile(relPath string) bool {
	// Check directory components.
	parts := strings.Split(relPath, "/")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "test", "tests", "__tests__", "spec":
			return true
		}
	}

	base := parts[len(parts)-1]

	// Go: _test.go
	if strings.HasSuffix(base, "_test.go") {
		return true
	}

	// Python: test_*.py and *_test.py
	if strings.HasSuffix(base, ".py") {
		name := strings.TrimSuffix(base, ".py")
		if strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test") {
			return true
		}
	}

	// JS/TS: *.test.* and *.spec.*
	ext := filepath.Ext(base)
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".mts", ".cjs", ".cts":
		nameNoExt := strings.TrimSuffix(base, ext)
		if strings.HasSuffix(nameNoExt, ".test") || strings.HasSuffix(nameNoExt, ".spec") {
			return true
		}
	}

	// Java, Kotlin, C#, Ruby: base name (without extension) ends in Test, Tests or _spec.
	// The suffix must be preceded by at least one character ("Test.java" alone is not a test file).
	switch ext {
	case ".java", ".kt", ".cs", ".rb":
		nameNoExt := strings.TrimSuffix(base, ext)
		if len(nameNoExt) > 4 && strings.HasSuffix(nameNoExt, "Test") {
			return true
		}
		if len(nameNoExt) > 5 && strings.HasSuffix(nameNoExt, "Tests") {
			return true
		}
		if len(nameNoExt) > 5 && strings.HasSuffix(nameNoExt, "_spec") {
			return true
		}
	}

	return false
}

// isExactMatch returns true when the query matches the target exactly under
// smart-case rules. An all-lowercase query that equals the target
// case-insensitively counts as exact.
func isExactMatch(query, target string, caseSensitive bool) bool {
	if caseSensitive {
		return query == target
	}
	return strings.EqualFold(query, target)
}

// rankSymbols sorts matches by the ranking tiers defined in 02-REQ-4.1:
//  1. exact before prefix (smart-case equality counts as exact)
//  2. exported before unexported
//  3. non-test files before test files
//  4. shorter path first
//  5. path (lexicographic)
//  6. StartLine
//
// The sort is stable so repeated calls return an identical order.
func rankSymbols(matches []SymbolMatch, query string, qualified bool, caseSensitive bool) {
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]

		// Tier 1: exact before prefix.
		aTarget := a.Name
		bTarget := b.Name
		if qualified {
			if a.Container != "" {
				aTarget = a.Container + "." + a.Name
			}
			if b.Container != "" {
				bTarget = b.Container + "." + b.Name
			}
		}
		aExact := isExactMatch(query, aTarget, caseSensitive)
		bExact := isExactMatch(query, bTarget, caseSensitive)
		if aExact != bExact {
			return aExact
		}

		// Tier 2: exported before unexported.
		if a.Exported != b.Exported {
			return a.Exported
		}

		// Tier 3: non-test files before test files.
		aTest := isTestFile(a.Path)
		bTest := isTestFile(b.Path)
		if aTest != bTest {
			return !aTest
		}

		// Tier 4: shorter path first.
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}

		// Tier 5: path (lexicographic).
		if a.Path != b.Path {
			return a.Path < b.Path
		}

		// Tier 6: StartLine.
		return a.StartLine < b.StartLine
	})
}

// sanitizePath replaces control characters in a path with '?' for display.
func sanitizePath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
