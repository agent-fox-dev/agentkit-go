package outline

import "strings"

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
	LangSwift      = "Swift"
	LangScala      = "Scala"
	LangLua        = "Lua"
	LangShell      = "Shell"
	LangPerl       = "Perl"
)

// extToLang maps file extensions (lower-cased, with leading dot) to language
// names. The Go, Python, JS/TS, Rust, Java, Kotlin, C#, Ruby and C/C++
// extensions get heuristic support; PHP, Swift, Scala, Lua, Shell and Perl
// are ctags-only (they appear in the table so their files are read and passed
// to ctags, but have no heuristic backend).
var extToLang = map[string]string{
	// Go
	".go": LangGo,

	// Python
	".py":  LangPython,
	".pyw": LangPython,

	// JavaScript
	".js":  LangJavaScript,
	".jsx": LangJavaScript,
	".mjs": LangJavaScript,
	".cjs": LangJavaScript,

	// TypeScript
	".ts":  LangTypeScript,
	".tsx": LangTypeScript,
	".mts": LangTypeScript,
	".cts": LangTypeScript,

	// Rust
	".rs": LangRust,

	// Java
	".java": LangJava,

	// Kotlin
	".kt":  LangKotlin,
	".kts": LangKotlin,

	// C#
	".cs": LangCSharp,

	// Ruby
	".rb": LangRuby,

	// C
	".c": LangC,
	".h": LangC,

	// C++
	".cpp": LangCPP,
	".cxx": LangCPP,
	".cc":  LangCPP,
	".hpp": LangCPP,
	".hxx": LangCPP,
	".hh":  LangCPP,

	// ctags-only languages
	".php":   LangPHP,
	".swift": LangSwift,
	".scala": LangScala,
	".lua":   LangLua,
	".sh":    LangShell,
	".bash":  LangShell,
	".zsh":   LangShell,
	".pl":    LangPerl,
	".pm":    LangPerl,
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
