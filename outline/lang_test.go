package outline

import "testing"

func TestLangForExtLooksUpTheTableInAnyCase(t *testing.T) {
	for ext, want := range map[string]string{
		".go": LangGo, ".JSX": LangJavaScript, ".mts": LangTypeScript, ".kts": LangKotlin, ".pyw": LangPython,
		".zsh": LangShell, ".hxx": LangCPP, ".h": LangC, ".Rs": LangRust, ".sc": LangScala, ".cs": LangCSharp,
		".txt": "", "": "", ".md": "", ".json": "",
	} {
		if got := LangForExt(ext); got != want {
			t.Errorf("LangForExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

// A .h header is C unless its content is C++; other extensions are not
// sniffed.
func TestLangForTellsACppHeaderFromAC(t *testing.T) {
	for _, c := range []struct{ path, src, want string }{
		{"a.h", "#include <stdio.h>\nstruct point { int x; };\nint add(int a, int b);\n", LangC},
		{"a.h", "typedef struct foo { int a; } foo_t;\n", LangC},
		{"a.h", "#ifdef __cplusplus\nextern \"C\" {\n#endif\nvoid f(void);\n", LangC},
		{"a.h", "", LangC},
		{"a.h", "namespace ns {\nint f();\n}\n", LangCPP},
		{"a.H", "class Widget {\npublic:\n  void draw();\n};\n", LangCPP},
		{"a.h", "template <typename T>\nT max(T a, T b);\n", LangCPP},
		{"a.h", "enum class Color { Red };\n", LangCPP},
		{"a.h", "using namespace std;\n", LangCPP},
		{"a.c", "class X {};", LangC},
		{"a.go", "", LangGo},
	} {
		if got := LangFor("/x/"+c.path, []byte(c.src)); got != c.want {
			t.Errorf("LangFor(%s, %q) = %q, want %q", c.path, c.src, got, c.want)
		}
	}
}
