package main

import (
	"flag"
	"slices"
	"strings"
	"testing"
)

// TS-19-55: the token file yields one trimmed token per non-blank,
// non-comment line, and tokens come from no flag or environment variable.
// 19-REQ-8.3.
func TestTokenFile_TS19_55(t *testing.T) {
	tokA, tokB, tokC := tok40, strings.ToUpper(tok40[:20])+tok40[20:], "c"+tok40[1:]
	dir := t.TempDir()
	f1 := writeFile(t, dir, "tokens", "# comment\n\n   "+tokA+"   \n\t"+tokB+"\n#"+tokC+"\n", 0o600)
	toks, err := readTokenFile(f1)
	if err != nil || !slices.Equal(toks, []string{tokA, tokB}) {
		t.Fatalf("readTokenFile = %q, %v; want [tokA tokB]", toks, err)
	}
	short := "short-token-20-chars"
	f2 := writeFile(t, dir, "short", short+"\n", 0o600)
	if _, err := readTokenFile(f2); err == nil || strings.Contains(err.Error(), short) {
		t.Errorf("a 20-character token gave %v, want a refusal that does not contain it", err)
	}
	var o options
	var network string
	newFlagSet(&o, &network).VisitAll(func(f *flag.Flag) {
		if f.Name != "token-file" && strings.Contains(strings.ToLower(f.Name), "token") {
			t.Errorf("flag --%s mentions a token", f.Name)
		}
	})
	t.Setenv("AGENTKIT_GATEWAY_TOKEN", tokA)
	if code, _, _ := runCmd("--plaintext"); code != 2 {
		t.Errorf("a run with the token only in the environment exited %d, want 2", code)
	}
}
