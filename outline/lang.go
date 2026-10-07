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
	LangSwift      = "Swift"
	LangScala      = "Scala"
	LangLua        = "Lua"
	LangShell      = "Shell"
	LangPerl       = "Perl"

	// Languages with no heuristic, outlined by universal ctags alone.
	LangAda           = "Ada"
	LangClojure       = "Clojure"
	LangCOBOL         = "COBOL"
	LangCUDA          = "CUDA"
	LangD             = "D"
	LangElixir        = "Elixir"
	LangElm           = "Elm"
	LangEmacsLisp     = "Emacs Lisp"
	LangErlang        = "Erlang"
	LangFortran       = "Fortran"
	LangGDScript      = "GDScript"
	LangJulia         = "Julia"
	LangLisp          = "Lisp"
	LangOCaml         = "OCaml"
	LangObjectiveC    = "Objective-C"
	LangPascal        = "Pascal"
	LangPowerShell    = "PowerShell"
	LangProtobuf      = "Protocol Buffers"
	LangR             = "R"
	LangRaku          = "Raku"
	LangScheme        = "Scheme"
	LangSQL           = "SQL"
	LangSystemVerilog = "SystemVerilog"
	LangTcl           = "Tcl"
	LangTerraform     = "Terraform"
	LangThrift        = "Thrift"
	LangVHDL          = "VHDL"
	LangVim           = "Vim Script"
)

// extToLang maps file extensions (lower-cased, with leading dot) to language
// names. The Go, Python, JS/TS, Rust, Java, Kotlin, C#, Ruby and C/C++
// extensions get heuristic support. The rest are ctags-only: they appear in
// the table so their files are read and passed to ctags, but have no
// heuristic backend.
//
// The ctags-only list is the programming languages Universal Ctags 6.2 has a
// parser for, by the extensions it maps to them, less those where the
// extension is ambiguous (`.m` is MATLAB or Objective-C, `.v` Verilog or V,
// `.s` assembly or R) or the parser's output is not worth having (Haskell's
// reports type constructors as functions and misses ordinary functions).
// Markup and data formats (Markdown, JSON, YAML, HTML) are left out: their
// tags are headings and keys, not declarations. Swift and Scala are listed
// although Universal Ctags 6.2 has no parser for them, so that a ctags build
// that has one is used.
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

	// C. A `.h` header is C unless its content says C++; see LangFor.
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
	".php":     LangPHP,
	".swift":   LangSwift,
	".scala":   LangScala,
	".lua":     LangLua,
	".sh":      LangShell,
	".bash":    LangShell,
	".zsh":     LangShell,
	".pl":      LangPerl,
	".pm":      LangPerl,
	".ada":     LangAda,
	".adb":     LangAda,
	".ads":     LangAda,
	".clj":     LangClojure,
	".cljc":    LangClojure,
	".cljs":    LangClojure,
	".cbl":     LangCOBOL,
	".cob":     LangCOBOL,
	".cu":      LangCUDA,
	".cuh":     LangCUDA,
	".d":       LangD,
	".di":      LangD,
	".ex":      LangElixir,
	".exs":     LangElixir,
	".elm":     LangElm,
	".el":      LangEmacsLisp,
	".erl":     LangErlang,
	".hrl":     LangErlang,
	".f":       LangFortran,
	".for":     LangFortran,
	".f90":     LangFortran,
	".f95":     LangFortran,
	".f03":     LangFortran,
	".f08":     LangFortran,
	".gd":      LangGDScript,
	".jl":      LangJulia,
	".lisp":    LangLisp,
	".lsp":     LangLisp,
	".ml":      LangOCaml,
	".mli":     LangOCaml,
	".mm":      LangObjectiveC,
	".pas":     LangPascal,
	".ps1":     LangPowerShell,
	".psm1":    LangPowerShell,
	".proto":   LangProtobuf,
	".r":       LangR,
	".raku":    LangRaku,
	".rakumod": LangRaku,
	".scm":     LangScheme,
	".rkt":     LangScheme,
	".sql":     LangSQL,
	".sv":      LangSystemVerilog,
	".svh":     LangSystemVerilog,
	".tcl":     LangTcl,
	".tf":      LangTerraform,
	".thrift":  LangThrift,
	".vhd":     LangVHDL,
	".vhdl":    LangVHDL,
	".vim":     LangVim,
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
