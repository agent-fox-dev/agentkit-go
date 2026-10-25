package tarsafe

import "testing"

func TestCleanRel(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "."}, {".", "."}, {"a/./b/", "a/b"}, {"a/../b", "b"}, {"a/..", "."},
	} {
		if got, err := CleanRel(c.in); err != nil || got != c.want {
			t.Errorf("CleanRel(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"/abs", `\abs`, "..", "../x", "a/../../x"} {
		if got, err := CleanRel(in); err == nil {
			t.Errorf("CleanRel(%q) = %q, want an error", in, got)
		}
	}
}
