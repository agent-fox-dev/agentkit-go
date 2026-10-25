package tools

import (
	"errors"
	"strings"
	"testing"
)

func TestApplyEditsRejectsABatchWithThePinnedText(t *testing.T) {
	const notUnique = "Found %d occurrences of the string to replace. The text must be unique. " +
		"Please provide more context to make it unique."
	cases := []struct {
		name    string
		content string
		edits   []Edit
		code    string
		text    string // the whole rejection, or a prefix ending in "…"
	}{
		{"no edits", "x", nil, "empty", "No edits were provided."},
		{"an empty old_string", "x", []Edit{{OldString: "", NewString: "y"}}, "empty_old_string",
			"edits[0].old_string is empty. Provide the exact text to replace."},
		{"a non-unique old_string is not a replace-all", "x := 1\ny := 1\nz := 1\n",
			[]Edit{{OldString: "1", NewString: "2"}}, "not_unique", strings.Replace(notUnique, "%d", "3", 1)},
		{"overlapping occurrences are not unique", "aaa", []Edit{{OldString: "aa", NewString: "b"}},
			"not_unique", strings.Replace(notUnique, "%d", "2", 1)},
		// Every edit is checked for presence before any for uniqueness, so
		// the absent edits[1] is reported although edits[0] is earlier.
		{"absence is checked across the batch before uniqueness", "alpha\nalpha\nbravo\n",
			[]Edit{{OldString: "alpha", NewString: "x"}, {OldString: "zulu", NewString: "y"}},
			"not_found", "edits[1]: The string to replace was not found in the file.…"},
		{"a batch rejection names the edit", "one\ntwo\ntwo\n",
			[]Edit{{OldString: "one", NewString: "1"}, {OldString: "two", NewString: "2"}},
			"not_unique", "edits[1]: " + strings.Replace(notUnique, "%d", "2", 1)},
		{"a single edit carries no index", "one\n", []Edit{{OldString: "zulu", NewString: "z"}},
			"not_found", "The string to replace was not found in the file.…"},
		{"overlapping edits", "abcdef", []Edit{{OldString: "abcd", NewString: "X"}, {OldString: "cdef", NewString: "Y"}},
			"overlap", "edits[0] and edits[1] overlap. Merge them into one edit or target disjoint regions."},
		{"a no-op batch", "hello", []Edit{{OldString: "hello", NewString: "hello"}}, "noop",
			"The edits would leave the file unchanged."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, n, err := ApplyEdits(c.content, c.edits)
			var ee *EditError
			if !errors.As(err, &ee) {
				t.Fatalf("ApplyEdits = %q, %d, %v; want an *EditError", out, n, err)
			}
			if ee.Phase != c.code {
				t.Errorf("rejection %q, want %q", ee.Phase, c.code)
			}
			if prefix, ok := strings.CutSuffix(c.text, "…"); ok {
				if !strings.HasPrefix(ee.Error(), prefix) {
					t.Errorf("text = %q, want it to start %q", ee.Error(), prefix)
				}
			} else if ee.Error() != c.text {
				t.Errorf("text = %q, want %q", ee.Error(), c.text)
			}
		})
	}
}

func TestApplyEditsMatchesEveryEditAgainstTheOriginal(t *testing.T) {
	out, n, err := ApplyEdits("one two three", []Edit{
		{OldString: "one", NewString: "two"}, // creates a second "two"
		{OldString: "two", NewString: "ONE"}, // must match the original "two"
	})
	if err != nil || n != 2 || out != "two ONE three" {
		t.Fatalf("ApplyEdits = %q, %d, %v; want %q, 2", out, n, err, "two ONE three")
	}
}

func TestNotFoundSaysWhereTheNeedlesFirstLineIs(t *testing.T) {
	content := "func a() {\n\treturn 1\n}\n\nfunc a() {\n\treturn 2\n}\n\nfunc a() {\n\treturn 3\n}\n\nfunc a() {\n\treturn 4\n}\n"
	for _, c := range []struct{ old, want, not string }{
		{"func a() {\n\treturn 9\n}", "Its first line occurs at line 1, 5, 9, …; the lines after it differ from the file.", "13"},
		{"    return 4", "Its first line occurs at line 14 with different indentation.", ""},
		{"func zzz() {\n\treturn 1\n}", "Not even its first line occurs in the file; re-read the file before retrying.", ""},
		{"\n\n\n", "not found in the file.", "line"},
	} {
		_, _, err := ApplyEdits(content, []Edit{{OldString: c.old, NewString: "x"}})
		if err == nil || !strings.HasSuffix(err.Error(), c.want) || (c.not != "" && strings.Contains(err.Error(), c.not)) {
			t.Errorf("%q: rejection %v, want it to end %q", c.old, err, c.want)
		}
	}
	// A single-line needle whose fold is present but not unique in its batch.
	_, _, err := ApplyEdits("x := ‘a’\nb\n", []Edit{{OldString: "x := 'a'", NewString: "y"}, {OldString: "zz", NewString: "q"}})
	if err == nil || !strings.Contains(err.Error(), "edits[0]: The string to replace was not found in the file. A near match is at line 1") {
		t.Errorf("a near match is not named: %v", err)
	}
}

func TestEditNormalisationRoundTripsTheBOMAndCRLF(t *testing.T) {
	norm, bom, ending := NormalizeForEdit("\ufeffline one\r\nline two\r\n")
	if !bom || ending != CRLF || norm != "line one\nline two\n" {
		t.Fatalf("NormalizeForEdit = %q, %v, %v", norm, bom, ending)
	}
	// A needle copied with its CRs intact matches the normalised file, and
	// the replacement gets the file's CRLF back exactly once.
	out, _, err := ApplyEdits(norm, []Edit{{OldString: "line one\r\nline", NewString: "LINE 1\r\nLINE"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := Restore(out, bom, ending); got != "\ufeffLINE 1\r\nLINE two\r\n" {
		t.Fatalf("Restore = %q", got)
	}
	if n, bom, ending := NormalizeForEdit("plain\n"); n != "plain\n" || bom || ending != LF {
		t.Fatalf("an LF file without a BOM: %q %v %v", n, bom, ending)
	}
}
