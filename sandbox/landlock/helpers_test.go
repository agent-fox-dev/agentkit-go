package landlock

import "testing"

// newTestProvider is a provider built through newProvider with Linux seams,
// probe ABI 4, a temporary Root and a faked version run, so it works on
// every platform; mods adjust the seams.
func newTestProvider(t *testing.T, o Options, mods ...func(*seams)) *provider {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.AgentBinary == "" {
		o.AgentBinary = placeholderAgent(t)
	}
	s := testSeams(4, nil)
	for _, m := range mods {
		m(&s)
	}
	p, err := newProvider(o, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
