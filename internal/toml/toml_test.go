package toml

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) (*Table, []Diagnostic) {
	t.Helper()
	tbl, diags, err := ParseTOML([]byte(src))
	if err != nil {
		t.Fatalf("ParseTOML: %v", err)
	}
	return tbl, diags
}

func sub(t *testing.T, tbl *Table, path ...string) *Table {
	t.Helper()
	for _, p := range path {
		next, ok := tbl.Sub(p)
		if !ok {
			t.Fatalf("sub-table %q of %q missing", p, tbl.Name())
		}
		tbl = next
	}
	return tbl
}

func TestParseTOMLReadsTheSupportedValueForms(t *testing.T) {
	tbl, diags := mustParse(t, `
name = "reviewer"
hash = "counts # of open PRs"  # trailing note
equals = "set x = 1"
basic = "a\tb\n\u00e9"
literal = 'a\tb'
on = true
off = false
retries = -12
grouped = 1_000
plain = 30
f1 = 30.0
f2 = 6.626e-34
f3 = 1_000.5
f4 = 1e3
archetypes = [
  "coder",   # the default
  "planner",
]
empty = []
`)
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	for key, want := range map[string]Value{
		"name":       {Kind: KindString, Str: "reviewer"},
		"hash":       {Kind: KindString, Str: "counts # of open PRs"},
		"equals":     {Kind: KindString, Str: "set x = 1"},
		"basic":      {Kind: KindString, Str: "a\tb\né"},
		"literal":    {Kind: KindString, Str: `a\tb`},
		"on":         {Kind: KindBool, Bool: true},
		"off":        {Kind: KindBool},
		"retries":    {Kind: KindInt, Int: -12},
		"grouped":    {Kind: KindInt, Int: 1000},
		"plain":      {Kind: KindInt, Int: 30},
		"f1":         {Kind: KindFloat, Float: 30},
		"f2":         {Kind: KindFloat, Float: 6.626e-34},
		"f3":         {Kind: KindFloat, Float: 1000.5},
		"f4":         {Kind: KindFloat, Float: 1000},
		"archetypes": {Kind: KindStringArray, Array: []string{"coder", "planner"}},
		"empty":      {Kind: KindStringArray, Array: []string{}},
	} {
		got, ok := tbl.Get(key)
		if !ok {
			t.Errorf("%s missing", key)
			continue
		}
		got.Line = 0
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v, want %+v", key, got, want)
		}
	}
	tbl, _ = mustParse(t, "a = -inf\nb = nan\n")
	if v, _ := tbl.Get("a"); v.Kind != KindFloat || !math.IsInf(v.Float, -1) {
		t.Errorf("a = %+v, want -inf", v)
	}
	if v, _ := tbl.Get("b"); v.Kind != KindFloat || !math.IsNaN(v.Float) {
		t.Errorf("b = %+v, want nan", v)
	}
}

func TestParseTOMLNestsHeadersAndDottedKeys(t *testing.T) {
	tbl, _ := mustParse(t, `
name = "x"
skill.dotted.key = "d"

[skill.tools]
module = "example.com/m"

[skill.session]
max_turns_add = 5
`)
	if tbl.Name() != "" || tbl.Line() != 0 || tbl.Qualify("k") != "k" {
		t.Errorf("root: name %q line %d qualify %q", tbl.Name(), tbl.Line(), tbl.Qualify("k"))
	}
	skill := sub(t, tbl, "skill")
	if got := strings.Join(skill.SubTables(), ","); got != "dotted,tools,session" {
		t.Errorf("SubTables() = %q, want written order", got)
	}
	if v, _ := sub(t, skill, "dotted").Get("key"); v.Str != "d" {
		t.Errorf("skill.dotted.key = %+v", v)
	}
	tools := sub(t, skill, "tools")
	if tools.Name() != "skill.tools" || tools.Qualify("module") != "skill.tools.module" {
		t.Errorf("name %q qualify %q", tools.Name(), tools.Qualify("module"))
	}
	if v, _ := tools.Get("module"); v.Str != "example.com/m" {
		t.Errorf("module = %+v", v)
	}
	if v, _ := sub(t, skill, "session").Get("max_turns_add"); v.Int != 5 {
		t.Errorf("max_turns_add = %+v", v)
	}
}

func TestParseTOMLSkipsAnUnsupportedValueWithAWarning(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"datetime", `built = 1979-05-27T07:32:00Z`, "dates"},
		{"local date", `built = 1979-05-27`, "dates"},
		{"inline table", `t = { a = "b" }`, "inline tables"},
		{"multi-line basic string", "s = \"\"\"a\nb\"\"\"", "multi-line"},
		{"multi-line literal string", "s = '''a\nb'''", "multi-line"},
		{"hex integer", `mask = 0xdeadbeef`, "decimal"},
		{"octal integer", `mask = 0o755`, "decimal"},
		{"integer out of range", `big = 99999999999999999999`, "unrecognized value"},
		{"mixed array", `a = ["x", 3]`, "arrays of strings"},
		{"nested array", `a = [["x"]]`, "arrays of strings"},
		{"array with a bad element", `a = ["""x"""]`, "multi-line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl, diags := mustParse(t, tc.src+"\ndescription = \"survives\"\n")
			if got := tbl.Keys(); len(got) != 1 || got[0] != "description" {
				t.Fatalf("Keys() = %v, want only the later key", got)
			}
			if len(diags) != 1 || diags[0].Severity != SeverityWarning || diags[0].Line != 1 ||
				!strings.Contains(diags[0].Message, tc.want) {
				t.Fatalf("diagnostics = %+v, want one line-1 warning mentioning %q", diags, tc.want)
			}
		})
	}
}

func TestParseTOMLReturnsASyntaxErrorWithItsLine(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		line      int
	}{
		{"array header not closed", "name = \"a\"\n[[servers]\n", 2},
		{"empty array header", "[[]]\n", 1},
		{"unterminated string", "name = \"a\ndescription = \"b\"\n", 1},
		{"unterminated array", "a = [\"x\",\n", 1},
		{"missing equals", "name \"a\"\n", 1},
		{"garbage after value", "name = \"a\" oops\n", 1},
		{"unclosed header", "[skill\n", 1},
		{"nesting past the depth bound", "a = " + strings.Repeat("[", 200_000), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl, _, err := ParseTOML([]byte(tc.src))
			var se *SyntaxError
			if !errors.As(err, &se) || tbl != nil {
				t.Fatalf("got (%v, %v), want a *SyntaxError and no table", tbl, err)
			}
			if se.Line != tc.line || se.Error() != fmt.Sprintf("line %d: %s", tc.line, se.Msg) {
				t.Fatalf("line = %d, Error() = %q, want line %d", se.Line, se.Error(), tc.line)
			}
		})
	}
}

func TestParseTOMLDuplicateKeyWarnsAndTheLastValueWins(t *testing.T) {
	tbl, diags := mustParse(t, "[s]\nd = \"first\"\nd = \"second\"\n")
	s := sub(t, tbl, "s")
	if v, _ := s.Get("d"); v.Str != "second" {
		t.Fatalf("d = %q", v.Str)
	}
	if len(s.Keys()) != 1 {
		t.Fatalf("Keys() = %v, want the key recorded once", s.Keys())
	}
	if len(diags) != 1 || diags[0].Line != 3 || !strings.Contains(diags[0].Message, `duplicate key "s.d"`) {
		t.Fatalf("diagnostics = %+v", diags)
	}
}

func TestParseTOMLStripsALeadingBOM(t *testing.T) {
	tbl, _ := mustParse(t, "\ufeffdescription = \"x\"")
	if _, ok := tbl.Get("description"); !ok {
		t.Fatalf("keys = %v; the BOM was read as part of the key", tbl.Keys())
	}
}

func TestParseTOMLKeepsKeysInWrittenOrder(t *testing.T) {
	tbl, _ := mustParse(t, "zulu = \"1\"\nalpha = \"2\"\nmike = \"3\"\n")
	if got := strings.Join(tbl.Keys(), ","); got != "zulu,alpha,mike" {
		t.Fatalf("Keys() = %q, want written order", got)
	}
}

func TestParseTOMLRecordsTheLineOfValuesTablesAndWarnings(t *testing.T) {
	tbl, diags := mustParse(t, "# c\nname = \"x\"\n\n[skill.tools]\nmodule = \"m\"\nwhen = 1979-05-27\n")
	if v, _ := tbl.Get("name"); v.Line != 2 {
		t.Errorf("name line = %d, want 2", v.Line)
	}
	tools := sub(t, tbl, "skill", "tools")
	if tools.Line() != 4 {
		t.Errorf("[skill.tools] line = %d, want 4", tools.Line())
	}
	if v, _ := tools.Get("module"); v.Line != 5 {
		t.Errorf("module line = %d, want 5", v.Line)
	}
	if len(diags) != 1 || diags[0].Line != 6 || !strings.Contains(diags[0].Message, `"skill.tools.when"`) {
		t.Errorf("diags = %+v, want one warning on line 6 naming skill.tools.when", diags)
	}
}

func TestParseTOMLArraysOfTablesKeepDocumentOrderAndTheirOwnKeys(t *testing.T) {
	root, _ := mustParse(t, `
[mcp]
enabled = true

[[mcp.servers]]
name = "github"

[mcp.servers.env]
TOKEN = "a"

[[mcp.servers]]
name = "db"
allow_sampling = true

[mcp.servers.env]
TOKEN = "b"

[[mcp.tools]]
`)
	mcp := sub(t, root, "mcp")
	if v, ok := mcp.Get("enabled"); !ok || !v.Bool {
		t.Fatal("a value beside an array of tables must still be readable")
	}
	if got := strings.Join(mcp.ArrayKeys(), ","); got != "servers,tools" {
		t.Fatalf("ArrayKeys() = %q", got)
	}
	servers, ok := mcp.Array("servers")
	if !ok || len(servers) != 2 {
		t.Fatalf("servers = %d, want 2", len(servers))
	}
	for i, want := range []struct{ name, token string }{{"github", "a"}, {"db", "b"}} {
		if v, _ := servers[i].Get("name"); v.Str != want.name {
			t.Errorf("servers[%d].name = %q, want %q", i, v.Str, want.name)
		}
		if v, _ := sub(t, servers[i], "env").Get("TOKEN"); v.Str != want.token {
			t.Errorf("servers[%d].env.TOKEN = %q, want %q", i, v.Str, want.token)
		}
		if servers[i].Name() != "mcp.servers" {
			t.Errorf("servers[%d].Name() = %q", i, servers[i].Name())
		}
	}
	if _, leaked := servers[0].Get("allow_sampling"); leaked {
		t.Fatal("a key from the second element leaked into the first")
	}
	if _, leaked := mcp.Sub("servers"); leaked {
		t.Fatal("[mcp.servers.env] created a sub-table beside the array instead of under its element")
	}
}

func TestNilTableLookupsFindNothing(t *testing.T) {
	var tbl *Table
	_, okGet := tbl.Get("k")
	_, okSub := tbl.Sub("k")
	_, okArr := tbl.Array("k")
	if okGet || okSub || okArr || tbl.ArrayKeys() != nil {
		t.Fatal("a nil table must answer every lookup with not found")
	}
}
