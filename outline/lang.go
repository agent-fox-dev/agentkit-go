package outline

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Language names returned by langForExt.
const (
	LangGo         = "Go"
	LangPython     = "Python"
	LangJavaScript = "JavaScript"
	LangTypeScript = "TypeScript"
	LangRust       = "Rust"
	LangJava       = "Java"
	LangKotlin     = "Kotlin"
	LangCSharp     = "C#"
	LangRuby       = "Ruby"
	LangC          = "C"
	LangCPP        = "C++"
	LangPHP        = "PHP"
	LangScala      = "Scala"
	LangLua        = "Lua"
	LangShell      = "Shell"
)

// extToLang maps file extensions (lower-cased, with leading dot) to language
// names: Go, and the languages with a tree-sitter grammar (see grammars).
// Markup and data formats are left out: their "declarations" are headings
// and keys.
var extToLang = map[string]string{
	".go":    LangGo,
	".py":    LangPython,
	".pyw":   LangPython,
	".js":    LangJavaScript,
	".jsx":   LangJavaScript,
	".mjs":   LangJavaScript,
	".cjs":   LangJavaScript,
	".ts":    LangTypeScript,
	".tsx":   LangTypeScript,
	".mts":   LangTypeScript,
	".cts":   LangTypeScript,
	".rs":    LangRust,
	".java":  LangJava,
	".kt":    LangKotlin,
	".kts":   LangKotlin,
	".cs":    LangCSharp,
	".rb":    LangRuby,
	".c":     LangC,
	".h":     LangC, // C unless its content says C++; see LangFor
	".cpp":   LangCPP,
	".cxx":   LangCPP,
	".cc":    LangCPP,
	".hpp":   LangCPP,
	".hxx":   LangCPP,
	".hh":    LangCPP,
	".php":   LangPHP,
	".scala": LangScala,
	".sc":    LangScala,
	".lua":   LangLua,
	".sh":    LangShell,
	".bash":  LangShell,
	".zsh":   LangShell,
}

// langForExt returns the language name for a file extension, or "" if the
// extension is not in the table. The extension should include the leading dot.
func langForExt(ext string) string {
	return extToLang[strings.ToLower(ext)]
}

// LangForExt returns the language name for a file extension (with the leading
// dot, in any case), or "" if the extension is not in the table. It is the
// exported form of the table's lookup, so a package that names languages can
// use the table instead of keeping a copy.
func LangForExt(ext string) string {
	return langForExt(ext)
}

// cppHeaderMarker matches a line that only C++ can contain: a class or
// namespace declaration, a template, an access specifier, a using
// directive, or an `enum class`.
var cppHeaderMarker = regexp.MustCompile(`(?m)^\s*(?:(?:class|namespace|template)\b[^;(]*[{<:]|template\s*<|(?:public|private|protected)\s*:|using\s+namespace\b|enum\s+(?:class|struct)\b)`)

// LangFor returns the language of the file at path, whose content is src:
// the extension table's answer, except that a `.h` header containing C++
// is C++. src may be nil, in which case the extension alone decides.
func LangFor(path string, src []byte) string {
	ext := filepath.Ext(path)
	lang := langForExt(ext)
	if lang == LangC && strings.EqualFold(ext, ".h") && cppHeaderMarker.Match(src) {
		return LangCPP
	}
	return lang
}
