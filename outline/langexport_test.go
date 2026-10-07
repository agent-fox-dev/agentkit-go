package outline

import "testing"

// LangForExt is the exported lookup of the extension table, so a package that
// names languages (the codesearch index) can reuse the table instead of
// keeping a copy that drifts.
func TestLangForExtIsTheTableLookup(t *testing.T) {
	for ext, want := range map[string]string{
		".go": LangGo, ".JSX": LangJavaScript, ".mts": LangTypeScript,
		".kts": LangKotlin, ".pyw": LangPython, ".zsh": LangShell, ".hxx": LangCPP,
		".txt": "", "": "", ".unknown": "",
	} {
		if got := LangForExt(ext); got != want {
			t.Errorf("LangForExt(%q) = %q, want %q", ext, got, want)
		}
	}
	for ext := range extToLang {
		if LangForExt(ext) != langForExt(ext) {
			t.Errorf("LangForExt(%q) disagrees with the table", ext)
		}
	}
}

// Issue #73 §3: the table covers the programming languages universal ctags
// has a parser for, so ctags is asked about them instead of every such file
// being returned as `none` unread.
func TestTheTableCoversCtagsLanguages(t *testing.T) {
	for ext, want := range map[string]string{
		".ex": LangElixir, ".exs": LangElixir, ".ml": LangOCaml, ".clj": LangClojure,
		".mm": LangObjectiveC, ".proto": LangProtobuf, ".sql": LangSQL, ".tf": LangTerraform,
		".erl": LangErlang, ".jl": LangJulia, ".elm": LangElm, ".ps1": LangPowerShell,
		".thrift": LangThrift, ".f90": LangFortran, ".ads": LangAda, ".cu": LangCUDA,
		".gd": LangGDScript, ".d": LangD, ".r": LangR,
		// Left out on purpose: ambiguous, or markup.
		".m": "", ".v": "", ".md": "", ".json": "",
	} {
		if got := LangForExt(ext); got != want {
			t.Errorf("LangForExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

// Issue #73 §5: a `.h` header is C unless its content is C++, so a C++
// project's headers are not labelled C.
func TestLangForTellsACppHeaderFromAC(t *testing.T) {
	for src, want := range map[string]string{
		"#include <stdio.h>\nstruct point { int x; };\nint add(int a, int b);\n": LangC,
		"typedef struct foo { int a; } foo_t;\n":                                 LangC,
		"#ifdef __cplusplus\nextern \"C\" {\n#endif\nvoid f(void);\n":            LangC,
		"namespace ns {\nint f();\n}\n":                                          LangCPP,
		"class Widget {\npublic:\n  void draw();\n};\n":                          LangCPP,
		"template <typename T>\nT max(T a, T b);\n":                              LangCPP,
		"enum class Color { Red };\n":                                            LangCPP,
		"using namespace std;\n":                                                 LangCPP,
	} {
		if got := LangFor("/x/a.h", []byte(src)); got != want {
			t.Errorf("LangFor(a.h, %q) = %q, want %q", src, got, want)
		}
	}
	if got := LangFor("/x/a.h", nil); got != LangC {
		t.Errorf("LangFor(a.h, nil) = %q, want C", got)
	}
	if got := LangFor("/x/a.c", []byte("class X {};")); got != LangC {
		t.Errorf("LangFor(a.c, ...) = %q: only a header is sniffed", got)
	}
	if got := LangFor("/x/a.go", nil); got != LangGo {
		t.Errorf("LangFor(a.go) = %q", got)
	}
}
