package gitclone_test

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

// TS-17-6: for any accepted repository and any dir, the clone argv starts
// with the fixed protocol prefix, ends with -- URL dir, and holds nothing
// between them but --depth N and --branch REF, each exactly when set.
// 17-REQ-1.6.
func TestArgsProperty_TS17_6(t *testing.T) {
	r := rand.New(rand.NewPCG(17, 6))
	const refAlphabet = "abcXYZ019._-/"
	word := func(alphabet string, n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		return string(b)
	}
	fixed := []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "clone"}
	accepted := 0
	for range 5000 {
		repo := sandbox.GitRepo{
			URL:   "https://" + word("abcdefghij", 1+r.IntN(8)) + ".example/" + word("abcdef/-_.", r.IntN(12)),
			Depth: r.IntN(1001),
		}
		if r.IntN(3) > 0 {
			repo.Ref = word(refAlphabet, 1+r.IntN(10))
		}
		dir := "/" + word("abcdef/", 1+r.IntN(10))
		if gitclone.Check(repo) != nil {
			continue
		}
		accepted++
		a := gitclone.Args(repo, dir)
		if len(a) < 8 || !slices.Equal(a[:5], fixed) || !slices.Equal(a[len(a)-3:], []string{"--", repo.URL, dir}) {
			t.Fatalf("Args(%+v, %q) = %q", repo, dir, a)
		}
		mid := a[5 : len(a)-3]
		allowed := []string{"--depth", strconv.Itoa(repo.Depth), "--branch", repo.Ref}
		for _, e := range mid {
			if !slices.Contains(allowed, e) {
				t.Fatalf("Args(%+v) has %q between clone and --: %q", repo, e, a)
			}
		}
		if slices.Contains(mid, "--depth") != (repo.Depth > 0) || slices.Contains(mid, "--branch") != (repo.Ref != "") {
			t.Fatalf("Args(%+v) = %q: --depth or --branch present when unset, or missing when set", repo, a)
		}
	}
	if accepted < 1000 {
		t.Fatalf("only %d of 5000 generated repositories were accepted; the generator is too narrow", accepted)
	}
}
