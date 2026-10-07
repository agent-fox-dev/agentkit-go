package outline

import (
	"bytes"
	"regexp"
	"strings"
)

// An identifier is letters, digits and underscore in ANY script. Go's \w is
// ASCII, so a rule written with it cut `def café()` to `caf` and found no
// `class Ölfass` at all; rx and jsRx compile every rule with \w widened.
// JavaScript and TypeScript add `$`, which is an identifier character there
// (`$init`, a jQuery or Svelte `$store`).
const (
	identChars   = `\p{L}\p{N}_`
	jsIdentChars = identChars + `$`
)

// rx compiles a heuristic pattern with a Unicode \w.
func rx(pattern string) *regexp.Regexp { return regexp.MustCompile(widenWord(pattern, identChars)) }

// jsRx compiles a JavaScript or TypeScript pattern, whose \w also takes `$`.
func jsRx(pattern string) *regexp.Regexp {
	return regexp.MustCompile(widenWord(pattern, jsIdentChars))
}

// widenWord rewrites every \w in pattern as the class of chars: `[chars]`
// outside a bracket expression and the bare chars inside one.
func widenWord(pattern, chars string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			if pattern[i+1] == 'w' {
				if inClass {
					b.WriteString(chars)
				} else {
					b.WriteString("[" + chars + "]")
				}
			} else {
				b.WriteByte(c)
				b.WriteByte(pattern[i+1])
			}
			i++
			continue
		case c == '[' && !inClass:
			inClass = true
		case c == ']' && inClass:
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// heuristicLangs maps language names to their heuristic rule sets.
// Only the ten languages with heuristic support are listed.
var heuristicLangs = map[string]heuristicLang{
	LangPython:     {rules: pythonRules},
	LangJavaScript: {rules: javascriptRules},
	LangTypeScript: {rules: typescriptRules},
	LangRust:       {rules: rustRules},
	LangJava:       {rules: javaRules},
	LangKotlin:     {rules: kotlinRules},
	LangCSharp:     {rules: csharpRules, scope: namespaceBlock},
	LangRuby:       {rules: rubyRules},
	LangC:          {rules: cRules, typedefs: true},
	LangCPP:        {rules: cppRules, scope: namespaceBlock, typedefs: true},
}

// heuristicLang is one language's heuristic: its rules, and the two pieces
// of state a line-at-a-time scan needs for C-family code.
type heuristicLang struct {
	rules []heuristicRule
	// scope matches the opener of a block whose body holds top-level
	// declarations, one indent level in: a C# block-scoped namespace, whose
	// contents are conventionally indented, or a C++ namespace.
	scope *regexp.Regexp
	// typedefs reports a multi-line `typedef struct { ... } Name;` under the
	// name on its closing line.
	typedefs bool
}

// heuristicRule describes one anchored regex pattern for a language.
type heuristicRule struct {
	re           *regexp.Regexp
	kind         Kind
	nameIdx      int // submatch index for the declaration name
	containerIdx int // submatch index for the container, 0 for none
	exported     func(line string, name string) bool
}

// Exported rules shared by several languages.
func notUnderscore(_, name string) bool { return !strings.HasPrefix(name, "_") }
func always(_, _ string) bool           { return true }
func never(_, _ string) bool            { return false }
func prefixed(p string) func(string, string) bool {
	return func(line, _ string) bool { return strings.HasPrefix(line, p) }
}

// namespaceBlock is the opener of a namespace with a body: `namespace X {`
// or `namespace X` with the brace on the next line, but not C#'s
// file-scoped `namespace X;`.
var namespaceBlock = rx(`^namespace\s+[\w.:]+\s*\{?\s*$`)

// --- Python ---
// def name(... and class Name...
// async def name(...
// Exported: true unless name starts with underscore.
var pythonRules = []heuristicRule{
	{re: rx(`^(?:async\s+)?def\s+(\w+)`), kind: KindFunc, nameIdx: 1, exported: notUnderscore},
	{re: rx(`^class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: notUnderscore},
}

// --- JavaScript ---
// function name(... / export function name(... / export default function name(...
// export async function name(... / async function name(... / function* gen(...
// class Name... / export class Name... / export default class Name...
// const name = (...) => / export const name = async x => / const name = function
// Exported: true when prefixed with export.
const jsExport = `^(?:(export)\s+)?(?:default\s+)?(?:declare\s+)?`

// jsArrow is a const, let or var bound to an arrow function or a function
// expression; a binding to any other value is not reported.
const jsArrow = jsExport + `(?:const|let|var)\s+(\w+)\s*(?::[^=]+)?=\s*(?:async\s+)?(?:function\b|(?:\([^)]*\)|\w+)\s*(?::[^=]+)?=>)`

var javascriptRules = []heuristicRule{
	{re: jsRx(jsExport + `(?:async\s+)?function\s*\*?\s*(\w+)`), kind: KindFunc, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsExport + `class\s+(\w+)`), kind: KindClass, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsArrow), kind: KindFunc, nameIdx: 2, exported: prefixed("export")},
}

// --- TypeScript ---
// Same as JavaScript plus abstract and declare, interface, enum and type.
var typescriptRules = []heuristicRule{
	{re: jsRx(jsExport + `(?:async\s+)?function\s*\*?\s*(\w+)`), kind: KindFunc, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsExport + `(?:abstract\s+)?class\s+(\w+)`), kind: KindClass, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsExport + `interface\s+(\w+)`), kind: KindInterface, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsExport + `(?:const\s+)?enum\s+(\w+)`), kind: KindEnum, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsExport + `type\s+(\w+)\s*(?:<[^=]*>)?\s*=`), kind: KindType, nameIdx: 2, exported: prefixed("export")},
	{re: jsRx(jsArrow), kind: KindFunc, nameIdx: 2, exported: prefixed("export")},
}

// --- Rust ---
// fn name(... / pub fn name(... / pub(crate) fn name(... and any of const,
// async, unsafe and extern "ABI" before fn
// struct Name... / pub struct Name...
// enum Name... / pub enum Name...
// trait Name... / pub trait Name...
// const NAME... / pub const NAME...
// macro_rules! name
// Exported: true when prefixed with pub. A macro_rules! macro is exported by
// a #[macro_export] attribute on another line, which a line rule cannot see,
// so it is reported as not exported.
const rustPub = `^(?:(pub(?:\([^)]*\))?)\s+)?`

var rustRules = []heuristicRule{
	{re: rx(rustPub + `(?:(?:const|async|unsafe|extern(?:\s+"[^"]*")?)\s+)*fn\s+(\w+)`), kind: KindFunc, nameIdx: 2, exported: prefixed("pub")},
	{re: rx(rustPub + `struct\s+(\w+)`), kind: KindType, nameIdx: 2, exported: prefixed("pub")},
	{re: rx(rustPub + `enum\s+(\w+)`), kind: KindEnum, nameIdx: 2, exported: prefixed("pub")},
	{re: rx(rustPub + `(?:unsafe\s+)?trait\s+(\w+)`), kind: KindTrait, nameIdx: 2, exported: prefixed("pub")},
	{re: rx(rustPub + `const\s+(\w+)`), kind: KindConst, nameIdx: 2, exported: prefixed("pub")},
	{re: rx(`^macro_rules!\s*(\w+)`), kind: KindMacro, nameIdx: 1, exported: never},
}

// --- Java ---
// class, interface, @interface, enum and record, after any of the class
// modifiers (public, protected, private, static, abstract, final, sealed,
// non-sealed, strictfp) in any order.
// Exported: true when prefixed with public.
const javaMods = `^(?:(?:public|protected|private|static|abstract|final|sealed|non-sealed|strictfp)\s+)*`

var javaRules = []heuristicRule{
	{re: rx(javaMods + `class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: prefixed("public")},
	{re: rx(javaMods + `@?interface\s+(\w+)`), kind: KindInterface, nameIdx: 1, exported: prefixed("public")},
	{re: rx(javaMods + `enum\s+(\w+)`), kind: KindEnum, nameIdx: 1, exported: prefixed("public")},
	{re: rx(javaMods + `record\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: prefixed("public")},
}

// --- Kotlin ---
// fun name(... including generic (`fun <T> name`) and extension
// (`fun Type.name`) functions; class, interface (and `fun interface`), enum
// class, object and typealias; each after any of Kotlin's modifiers.
// Exported: true unless private, internal or protected is among the
// modifiers — Kotlin's default visibility is public, and `public` is almost
// never written.
const kotlinMods = `^(?:(?:public|private|internal|protected|open|abstract|sealed|data|inline|value|annotation|inner|suspend|override|operator|infix|tailrec|external|expect|actual|final|const|lateinit)\s+)*`

var kotlinRules = []heuristicRule{
	{re: rx(kotlinMods + `enum\s+class\s+(\w+)`), kind: KindEnum, nameIdx: 1, exported: kotlinExported},
	{re: rx(kotlinMods + `(?:fun\s+)?interface\s+(\w+)`), kind: KindInterface, nameIdx: 1, exported: kotlinExported},
	{re: rx(kotlinMods + `fun\s+(?:<[^>]*>\s*)?(?:[\w<>?,. ]*\.)?(\w+)\s*\(`), kind: KindFunc, nameIdx: 1, exported: kotlinExported},
	{re: rx(kotlinMods + `class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: kotlinExported},
	{re: rx(kotlinMods + `object\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: kotlinExported},
	{re: rx(kotlinMods + `typealias\s+(\w+)`), kind: KindType, nameIdx: 1, exported: kotlinExported},
}

// kotlinModsRe is the run of modifiers in front of a Kotlin declaration.
var kotlinModsRe = regexp.MustCompile(kotlinMods)

// kotlinExported reports a declaration visible outside its module: any
// that no private, internal or protected modifier narrows.
func kotlinExported(line, _ string) bool {
	for _, m := range strings.Fields(kotlinModsRe.FindString(line)) {
		switch m {
		case "private", "internal", "protected":
			return false
		}
	}
	return true
}

// --- C# ---
// namespace, class, interface, enum, struct and record (class or struct),
// after any of C#'s type modifiers. Declarations in a block-scoped namespace
// are found one indent level in (see heuristicLang.scope).
// Exported: true when prefixed with public.
const csharpMods = `^(?:(?:public|private|protected|internal|static|abstract|sealed|partial|readonly|unsafe|new|file|ref)\s+)*`

var csharpRules = []heuristicRule{
	{re: rx(`^namespace\s+([\w.]+)`), kind: KindModule, nameIdx: 1, exported: always},
	{re: rx(csharpMods + `interface\s+(\w+)`), kind: KindInterface, nameIdx: 1, exported: prefixed("public")},
	{re: rx(csharpMods + `enum\s+(\w+)`), kind: KindEnum, nameIdx: 1, exported: prefixed("public")},
	{re: rx(csharpMods + `struct\s+(\w+)`), kind: KindType, nameIdx: 1, exported: prefixed("public")},
	{re: rx(csharpMods + `record\s+(?:class\s+|struct\s+)?(\w+)`), kind: KindClass, nameIdx: 1, exported: prefixed("public")},
	{re: rx(csharpMods + `class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: prefixed("public")},
}

// --- Ruby ---
// def name... / def self.name... / class Name... / module Name...
// A method name may end in ?, ! or =.
// Exported: true unless name starts with underscore.
var rubyRules = []heuristicRule{
	{re: rx(`^def\s+(?:self\.)?(\w+[?!=]?)`), kind: KindFunc, nameIdx: 1, exported: notUnderscore},
	{re: rx(`^class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: notUnderscore},
	{re: rx(`^module\s+(\w+)`), kind: KindModule, nameIdx: 1, exported: notUnderscore},
}

// --- C ---
// typedef (one line, a function pointer, or a braced body ending on the same
// line; a body over several lines is handled by heuristicLang.typedefs),
// return-type function-name(..., struct Name, enum Name, #define.
// Exported: always true.
//
// The typedef rules come first: `typedef void (*fn)(int);` would otherwise
// read as a function named void.
var cTypedefRules = []heuristicRule{
	{re: rx(`^typedef\b[^;{]*\(\s*\*\s*(\w+)\s*\)`), kind: KindType, nameIdx: 1, exported: always},
	{re: rx(`^typedef\b.*\}\s*\*?\s*(\w+)\s*;`), kind: KindType, nameIdx: 1, exported: always},
	{re: rx(`^typedef\b[^;{(]*?\b(\w+)\s*(?:\[[^\]]*\])?\s*;`), kind: KindType, nameIdx: 1, exported: always},
}

var cRules = append(append([]heuristicRule(nil), cTypedefRules...),
	heuristicRule{
		// Match: optional-type name( — a function definition at column 0.
		// This matches lines like "void hello(int x) {" or "int add(int a, int b) {"
		re:       rx(`^(?:(?:static|inline|extern|unsigned|signed|const|volatile)\s+)*(?:\w+[\s*]+)+(\w+)\s*\(`),
		kind:     KindFunc,
		nameIdx:  1,
		exported: always,
	},
	heuristicRule{re: rx(`^struct\s+(\w+)`), kind: KindType, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^enum\s+(\w+)`), kind: KindEnum, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^#define\s+(\w+)`), kind: KindMacro, nameIdx: 1, exported: always},
)

// --- C++ ---
// As C, plus class, namespace, `enum class`, return types with `::` and
// templates, and out-of-class definitions (`void Foo::bar()`,
// `Foo::Foo()`, `Foo::~Foo()`), which are methods of the qualifying class.
const cppFuncMods = `^(?:(?:static|inline|extern|unsigned|signed|const|volatile|virtual|constexpr|explicit)\s+)*`

var cppRules = append(append([]heuristicRule(nil), cTypedefRules...),
	heuristicRule{
		re:           rx(cppFuncMods + `(?:[\w:<>,]+[\s*&]+)*?((?:\w+::)+)(~?\w+)\s*\(`),
		kind:         KindMethod,
		nameIdx:      2,
		containerIdx: 1,
		exported:     always,
	},
	heuristicRule{
		// Function definitions at column 0.
		re:       rx(cppFuncMods + `(?:[\w:<>,]+[\s*&]+)+(\w+)\s*\(`),
		kind:     KindFunc,
		nameIdx:  1,
		exported: always,
	},
	heuristicRule{re: rx(`^class\s+(\w+)`), kind: KindClass, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^struct\s+(\w+)`), kind: KindType, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^namespace\s+([\w:]+)`), kind: KindModule, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^enum\s+(?:class\s+|struct\s+)?(\w+)`), kind: KindEnum, nameIdx: 1, exported: always},
	heuristicRule{re: rx(`^#define\s+(\w+)`), kind: KindMacro, nameIdx: 1, exported: always},
)

// typedefOpen is the first line of a typedef whose body spans lines, and
// typedefClose the line that names it.
var (
	typedefOpen  = rx(`^typedef\s+(?:struct|union|enum)\b[^;]*$`)
	typedefClose = rx(`^\}\s*\*?\s*(\w+)\s*;`)
)

// hasHeuristic returns true if the language has a heuristic backend.
func hasHeuristic(lang string) bool {
	_, ok := heuristicLangs[lang]
	return ok
}

// openScope is a namespace block being scanned: the indent of its opener
// and of its body, the latter known once the body's first line is seen.
type openScope struct {
	opener, body string
	bodyKnown    bool
}

// outlineHeuristic applies anchored line regular expressions to declarations
// starting at column 0 for the ten supported heuristic languages — or, in a
// C# or C++ namespace block, at the indent of the block's body.
// It returns (File, true) when the language has a heuristic, or (File{}, false)
// when it does not.
func outlineHeuristic(abs string, src []byte, opts Options) (File, bool) {
	lang := LangFor(abs, src)
	hl, ok := heuristicLangs[lang]
	if !ok {
		return File{}, false
	}

	// A UTF-8 byte-order mark is not part of line 1: left in, it stood in
	// front of the first declaration and no anchored rule matched it.
	src = bytes.TrimPrefix(src, []byte("\xEF\xBB\xBF"))
	lines := strings.Split(string(src), "\n")
	var decls []Decl
	var scopes []openScope
	typedefLine := 0 // 1-based line of an open multi-line typedef

	for lineNum, line := range lines {
		// Remove trailing \r for Windows line endings.
		line = strings.TrimRight(line, "\r")
		body := strings.TrimLeft(line, " \t")

		// Skip empty lines.
		if body == "" {
			continue
		}
		indent := line[:len(line)-len(body)]

		if len(scopes) > 0 {
			top := &scopes[len(scopes)-1]
			switch {
			case !top.bodyKnown && strings.HasPrefix(body, "{"):
				// The opener's brace on its own line.
				continue
			case strings.HasPrefix(body, "}") && indent == top.opener:
				scopes = scopes[:len(scopes)-1]
				continue
			case !top.bodyKnown:
				top.body, top.bodyKnown = indent, true
			}
		}

		// Only column 0, or the body indent of an open namespace block.
		if indent != "" && !inScopeBody(scopes, indent) {
			continue
		}

		if hl.typedefs {
			if typedefLine > 0 {
				if m := typedefClose.FindStringSubmatch(body); m != nil {
					decls = append(decls, Decl{
						Kind:      KindType,
						Name:      m[1],
						Signature: sanitiseSignature(strings.TrimSpace(lines[typedefLine-1]) + " … " + body),
						Exported:  true,
						StartLine: typedefLine,
					})
					typedefLine = 0
				}
				continue
			}
			if typedefOpen.MatchString(body) {
				typedefLine = lineNum + 1
				continue
			}
		}

		if hl.scope != nil && hl.scope.MatchString(body) {
			scopes = append(scopes, openScope{opener: indent})
		}

		for _, rule := range hl.rules {
			m := rule.re.FindStringSubmatch(body)
			if m == nil {
				continue
			}
			name := m[rule.nameIdx]
			if name == "" {
				continue
			}
			container := ""
			if rule.containerIdx > 0 {
				container = strings.TrimSuffix(m[rule.containerIdx], "::")
			}

			decls = append(decls, Decl{
				Kind:      rule.kind,
				Name:      name,
				Container: container,
				Signature: sanitiseSignature(body),
				Exported:  rule.exported(body, name),
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

// inScopeBody reports whether indent is the body indent of an open
// namespace block.
func inScopeBody(scopes []openScope, indent string) bool {
	for _, s := range scopes {
		if s.bodyKnown && s.body == indent {
			return true
		}
	}
	return false
}
