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
