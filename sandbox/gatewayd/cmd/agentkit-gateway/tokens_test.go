package main

import (
	"slices"
	"strings"
	"testing"
)

func TestTheTokenFileHoldsOneTrimmedTokenPerLine(t *testing.T) {
	tokB := strings.ToUpper(tok40)
	toks, err := readTokenFile(file(t, "# comment\n\n   "+tok40+"   \n\t"+tokB+"\n#c"+tok40+"\n"))
	if err != nil || !slices.Equal(toks, []string{tok40, tokB}) {
		t.Errorf("readTokenFile = %q, %v; want the two tokens", toks, err)
	}
	for name, body := range map[string]string{"short": "short-token-20-chars\n", "repeated": tok40 + "\n" + tok40 + "\n", "empty": "# none\n"} {
		if _, err := readTokenFile(file(t, body)); err == nil || strings.Contains(err.Error(), "short-token") || strings.Contains(err.Error(), tok40) {
			t.Errorf("%s: %v, want a refusal that does not contain the token", name, err)
		}
	}
	if _, err := readTokenFile("/nonexistent/tokens"); err == nil || !strings.HasPrefix(err.Error(), "token file: ") {
		t.Errorf("a missing file: %v, want a token file error", err)
	}
}
