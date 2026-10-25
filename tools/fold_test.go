package tools

import (
	"errors"
	"testing"
)

func TestTheWhitespaceTolerantFallback(t *testing.T) {
	const curly = "// “quoted” — dash … ellipsis"
	cases := []struct {
		name, content string
		edits         []Edit
		want          string // "" for a rejection
	}{
		{"an exact match is never overridden by a folded one", "a = 'x'\nb\na = ‘x’\n",
			[]Edit{{OldString: "a = 'x'", NewString: "a = 'y'"}}, "a = 'y'\nb\na = ‘x’\n"},
		{"smart quotes match their ASCII form", "func f() {\n\tname := “hello”\n}\n",
			[]Edit{{OldString: "\tname := \"hello\"", NewString: "\tname := \"bye\""}}, "func f() {\n\tname := \"bye\"\n}\n"},
		{"trailing whitespace is tolerated", "if x {\n\tdoIt()   \n}\n",
			[]Edit{{OldString: "\tdoIt() \n}", NewString: "\tdoThat()\n}"}}, "if x {\n\tdoThat()\n}\n"},
		{"leading whitespace is not", "if x {\n\t\tval := ‘a’\n}\n",
			[]Edit{{OldString: "    val := 'a'", NewString: "    val := 'b'"}}, ""},
		{"untouched lines keep their exact bytes", curly + "\ntarget := ‘v’\n" + curly + "\n",
			[]Edit{{OldString: "target := 'v'", NewString: "target := 'w'"}}, curly + "\ntarget := 'w'\n" + curly + "\n"},
		{"a non-unique folded match is rejected", "x := ‘a’\ny\nx := ‘a’\n",
			[]Edit{{OldString: "x := 'a'", NewString: "x := 'b'"}}, ""},
		{"a partially rescued batch is rejected whole", "a := ‘x’\nb := 2\n",
			[]Edit{{OldString: "a := 'x'", NewString: "a := 'y'"}, {OldString: "no such line", NewString: "boom"}}, ""},
		{"an exact edit in a folded batch must still be unique", "a := “q”\nreturn nil\nif x {\n\treturn nil // again\n}\n",
			[]Edit{{OldString: `a := "q"`, NewString: `a := "f"`}, {OldString: "return nil", NewString: "return err"}}, ""},
		{"an exact edit in a folded batch replaces only its bytes", "a := “q”\nreturn nil\nif x {\n\treturn nil // again\n}\n",
			[]Edit{{OldString: `a := "q"`, NewString: `a := "f"`}, {OldString: "nil // again", NewString: "err // again"}},
			"a := \"f\"\nreturn nil\nif x {\n\treturn err // again\n}\n"},
		{"folded edits that overlap are rejected", "a := ‘x’\nb := ‘y’\n",
			[]Edit{{OldString: "a := 'x'\nb := 'y'", NewString: "1"}, {OldString: "b := 'y'", NewString: "2"}}, ""},
		{"a folded no-op is rejected", "a := ‘x’\n", []Edit{{OldString: "a := 'x'", NewString: "a := ‘x’"}}, ""},
		// A needle that starts or ends with a newline must not claim the
		// bytes of a whitespace-only neighbour.
		{"a trailing newline keeps the next blank line", "a\nline2\n   \nline3\n",
			[]Edit{{OldString: "line2 \n", NewString: "new\n"}}, "a\nnew\n   \nline3\n"},
		{"a leading newline keeps the previous blank line", "a\n   \nline2\nline3\n",
			[]Edit{{OldString: "\nline2 ", NewString: "\nnew"}}, "a\n   \nnew\nline3\n"},
		{"a whitespace-only needle does not match by fold", "a\n \nb\n", []Edit{{OldString: " \n\n", NewString: "x"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := ApplyEdits(c.content, c.edits)
			if c.want == "" {
				var ee *EditError
				if !errors.As(err, &ee) {
					t.Fatalf("ApplyEdits = %q, %v; want a rejection", out, err)
				}
				return
			}
			if err != nil || out != c.want {
				t.Fatalf("ApplyEdits = %q, %v; want %q", out, err, c.want)
			}
		})
	}
	// The fallback reports the exact path's rejection, not one of its own.
	_, _, err := ApplyEdits("hello\n", []Edit{{OldString: "nothing like this", NewString: "x"}})
	if ee := (*EditError)(nil); !errors.As(err, &ee) || ee.Phase != "not_found" {
		t.Fatalf("err = %v, want the not_found rejection", err)
	}
}

func TestFoldConfusablesFoldsOnlyTheRenderedSet(t *testing.T) {
	for in, want := range map[string]string{
		"‘a’": "'a'", "“a”": `"a"`, "a–b": "a-b", "a—b": "a-b", "a−b": "a-b",
		"a\u00a0b": "a b", "a\u3000b": "a b", "a\u200bb": "ab", "a\ufeffb": "ab",
		"…": "...", "ﬁx": "fix", "ａｂ": "ab", "plain ascii": "plain ascii",
		// Unlisted runes are left alone: folding them would change meaning.
		"café": "café", "日本語": "日本語", "emoji 🙂": "emoji 🙂",
	} {
		if got := FoldConfusables(in); got != want {
			t.Errorf("FoldConfusables(%q) = %q, want %q", in, got, want)
		}
	}
	if got := FoldLine("x := ‘1’ \t\r"); got != "x := '1'" || FoldLine("  x") != "  x" {
		t.Errorf("FoldLine trims trailing whitespace and a CR, and only trailing: %q", got)
	}
}
