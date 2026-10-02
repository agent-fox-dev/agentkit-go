package outline

import (
	"regexp"
	"strings"
)

// heuristicLangs maps language names to their heuristic rule sets.
// Only the ten languages with heuristic support are listed.
var heuristicLangs = map[string][]heuristicRule{
	LangPython:     pythonRules,
	LangJavaScript: javascriptRules,
	LangTypeScript: typescriptRules,
	LangRust:       rustRules,
	LangJava:       javaRules,
	LangKotlin:     kotlinRules,
	LangCSharp:     csharpRules,
	LangRuby:       rubyRules,
	LangC:          cRules,
	LangCPP:        cppRules,
}

// heuristicRule describes one anchored regex pattern for a language.
type heuristicRule struct {
	re       *regexp.Regexp
	kind     Kind
	nameIdx  int  // submatch index for the declaration name
	exported func(line string, name string) bool
}

// --- Python ---
// def name(... and class Name...
// async def name(...
// Exported: true unless name starts with underscore.
var pythonRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:async\s+)?def\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 1,
		exported: func(_, name string) bool {
			return !strings.HasPrefix(name, "_")
		},
	},
	{
		re:      regexp.MustCompile(`^class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 1,
		exported: func(_, name string) bool {
			return !strings.HasPrefix(name, "_")
		},
	},
}

// --- JavaScript ---
// function name(... / export function name(... / export default function name(...
// export async function name(... / async function name(...
// class Name... / export class Name... / export default class Name...
// Exported: true when prefixed with export.
var javascriptRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?(?:async\s+)?function\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
}

// --- TypeScript ---
// Same as JavaScript plus interface and enum.
var typescriptRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?(?:async\s+)?function\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?interface\s+(\w+)`),
		kind:    KindInterface,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(export)\s+)?(?:default\s+)?enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "export")
		},
	},
}

// --- Rust ---
// fn name(... / pub fn name(... / pub(crate) fn name(...
// struct Name... / pub struct Name...
// enum Name... / pub enum Name...
// trait Name... / pub trait Name...
// const NAME... / pub const NAME...
// Exported: true when prefixed with pub.
var rustRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(pub(?:\([^)]*\))?)\s+)?fn\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "pub")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(pub(?:\([^)]*\))?)\s+)?struct\s+(\w+)`),
		kind:    KindType,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "pub")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(pub(?:\([^)]*\))?)\s+)?enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "pub")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(pub(?:\([^)]*\))?)\s+)?trait\s+(\w+)`),
		kind:    KindTrait,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "pub")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(pub(?:\([^)]*\))?)\s+)?const\s+(\w+)`),
		kind:    KindConst,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "pub")
		},
	},
}

// --- Java ---
// public class Name... / class Name...
// public interface Name... / interface Name...
// public enum Name... / enum Name...
// Exported: true when prefixed with public.
var javaRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(?:public|static)\s+)*class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(?:public|static)\s+)*interface\s+(\w+)`),
		kind:    KindInterface,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(?:public|static)\s+)*enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
}

// --- Kotlin ---
// public fun name(... / fun name(...
// public class Name... / class Name...
// public interface Name... / interface Name...
// public enum class Name... / enum class Name...
// Exported: true when prefixed with public.
var kotlinRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(public)\s+)?fun\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(public)\s+)?(?:data\s+)?class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(public)\s+)?interface\s+(\w+)`),
		kind:    KindInterface,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(public)\s+)?enum\s+class\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 2,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
}

// --- C# ---
// public class Name... / class Name...
// public interface Name... / interface Name...
// public enum Name... / enum Name...
// public struct Name... / struct Name...
// Exported: true when prefixed with public.
var csharpRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^(?:(?:public|static|internal|abstract|sealed)\s+)*class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(?:public|static|internal|abstract)\s+)*interface\s+(\w+)`),
		kind:    KindInterface,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(?:public|static|internal)\s+)*enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
	{
		re:      regexp.MustCompile(`^(?:(?:public|static|internal)\s+)*struct\s+(\w+)`),
		kind:    KindType,
		nameIdx: 1,
		exported: func(line, _ string) bool {
			return strings.HasPrefix(line, "public")
		},
	},
}

// --- Ruby ---
// def name... / class Name... / module Name...
// Exported: true unless name starts with underscore.
var rubyRules = []heuristicRule{
	{
		re:      regexp.MustCompile(`^def\s+(\w+)`),
		kind:    KindFunc,
		nameIdx: 1,
		exported: func(_, name string) bool {
			return !strings.HasPrefix(name, "_")
		},
	},
	{
		re:      regexp.MustCompile(`^class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 1,
		exported: func(_, name string) bool {
			return !strings.HasPrefix(name, "_")
		},
	},
	{
		re:      regexp.MustCompile(`^module\s+(\w+)`),
		kind:    KindModule,
		nameIdx: 1,
		exported: func(_, name string) bool {
			return !strings.HasPrefix(name, "_")
		},
	},
}

// --- C ---
// Return-type function-name(... patterns, struct Name, enum Name, typedef, #define
// Exported: always true.
var cRules = []heuristicRule{
	{
		// Match: optional-type name( — a function definition at column 0.
		// This matches lines like "void hello(int x) {" or "int add(int a, int b) {"
		re:      regexp.MustCompile(`^(?:(?:static|inline|extern|unsigned|signed|const|volatile)\s+)*(?:\w+[\s*]+)+(\w+)\s*\(`),
		kind:    KindFunc,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^struct\s+(\w+)`),
		kind:    KindType,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^#define\s+(\w+)`),
		kind:    KindMacro,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
}

// --- C++ ---
// Same as C plus class and namespace.
var cppRules = []heuristicRule{
	{
		// Function definitions at column 0.
		re:      regexp.MustCompile(`^(?:(?:static|inline|extern|unsigned|signed|const|volatile|virtual)\s+)*(?:\w+[\s*]+)+(\w+)\s*\(`),
		kind:    KindFunc,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^class\s+(\w+)`),
		kind:    KindClass,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^struct\s+(\w+)`),
		kind:    KindType,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^namespace\s+(\w+)`),
		kind:    KindModule,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^enum\s+(\w+)`),
		kind:    KindEnum,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
	{
		re:      regexp.MustCompile(`^#define\s+(\w+)`),
		kind:    KindMacro,
		nameIdx: 1,
		exported: func(_, _ string) bool {
			return true
		},
	},
}

// hasHeuristic returns true if the language has a heuristic backend.
func hasHeuristic(lang string) bool {
	_, ok := heuristicLangs[lang]
	return ok
}

// outlineHeuristic applies anchored line regular expressions to declarations
// starting at column 0 for the ten supported heuristic languages.
// It returns (File, true) when the language has a heuristic, or (File{}, false)
// when it does not.
func outlineHeuristic(abs string, src []byte, opts Options) (File, bool) {
	lang := langForExt(extOf(abs))
	rules, ok := heuristicLangs[lang]
	if !ok {
		return File{}, false
	}

	lines := strings.Split(string(src), "\n")
	var decls []Decl

	for lineNum, line := range lines {
		// Remove trailing \r for Windows line endings.
		line = strings.TrimRight(line, "\r")

		// Skip empty lines.
		if len(line) == 0 {
			continue
		}

		// Skip indented lines (not column 0).
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}

		for _, rule := range rules {
			m := rule.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[rule.nameIdx]
			if name == "" {
				continue
			}

			decls = append(decls, Decl{
				Kind:      rule.kind,
				Name:      name,
				Container: "", // always empty for heuristic
				Signature: sanitiseSignature(line),
				Exported:  rule.exported(line, name),
				StartLine: lineNum + 1, // 1-based
				EndLine:   0,           // always 0 for heuristic
			})
			break // first matching rule wins for this line
		}
	}

	if decls == nil {
		decls = []Decl{}
	}

	f := File{
		Path:    filePath(abs, opts.Root),
		Lang:    lang,
		Backend: BackendHeuristic,
		Decls:   decls,
	}
	return finishFile(f), true
}

// extOf returns the file extension (with leading dot) from an absolute path.
func extOf(abs string) string {
	for i := len(abs) - 1; i >= 0; i-- {
		if abs[i] == '.' {
			return strings.ToLower(abs[i:])
		}
		if abs[i] == '/' || abs[i] == '\\' {
			break
		}
	}
	return ""
}
