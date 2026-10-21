package providerconformance

import "testing"

// conformanceCases are the cases every provider runs, in the order they are
// described in the spec.
var conformanceCases = []conformanceCase{
	textCase,
	toolCallCase,
	toolCallStopCase,
	thinkingCase,
	cutOffCase,
	errorStatusCase,
	errorInStreamCase,
}

// TestProviderConformance runs every case against every provider as a subtest
// named <provider>/<case>, so a single one is selected with, for example,
// -run 'TestProviderConformance.*/ollama/thinking'. Each subtest has its own
// fake and its own environment, builds its agent with agentkit.New, and runs in
// parallel with the rest; none skips under -short.
func TestProviderConformance(t *testing.T) {
	for _, p := range providerTable {
		t.Run(p.name, func(t *testing.T) {
			for _, c := range conformanceCases {
				t.Run(c.name, func(t *testing.T) {
					t.Parallel()
					runCase(t, p, c, runOptions{})
				})
			}
		})
	}
}
