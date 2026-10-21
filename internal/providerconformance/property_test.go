package providerconformance

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

// randomTurn is a non-empty list of text, thinking and tool-call blocks with
// distinct tool ids and arguments that are JSON objects, ending with the
// vendor's plain stop or, when it holds a tool call, its tool-call stop.
//
// No two text blocks and no two thinking blocks are adjacent: OpenRouter and
// Ollama stream text as content deltas and thinking as reasoning deltas, with
// nothing to say where one block ends and the next of the same kind begins, so
// they are one block on those wires.
func randomTurn(rnd *rand.Rand, n int) logicalTurn {
	words := []string{"alpha", "beta", "gamma", "délta", "日本語", "x"}
	word := func() string { return words[rnd.Intn(len(words))] }
	var t logicalTurn
	prev := blockKind(-1)
	for i, size := 0, 1+rnd.Intn(4); i < size; i++ {
		kind := blockKind(rnd.Intn(3))
		if kind == prev && kind != blockToolCall {
			kind = blockToolCall
		}
		prev = kind
		switch kind {
		case blockText:
			t.blocks = append(t.blocks, textBlock(word()+" "+word()))
		case blockThinking:
			t.blocks = append(t.blocks, thinkingBlock("think "+word()+" "+word(), fmt.Sprintf("sig-%d-%d", n, i)))
		case blockToolCall:
			args := fmt.Sprintf(`{"v":%q,"n":%d,"nested":{"a":[1,2,%d]}}`, word(), rnd.Intn(100), i)
			t.blocks = append(t.blocks, toolBlock(fmt.Sprintf("call_%d_%d", n, i), "echo", args))
		}
	}
	t.end = endNormal
	if slices.ContainsFunc(t.blocks, func(b block) bool { return b.kind == blockToolCall }) && rnd.Intn(2) == 0 {
		t.end = endToolStop
	}
	return t
}

// TS-30-52 (30-REQ-3.2, 30-REQ-3.3): for any block list that ends normally,
// every provider's event skeleton equals provider/faux's, and every block-end
// event carries the whole scripted payload.
func TestSkeletonsEqualFauxForAnyBlockList_TS30_52(t *testing.T) {
	const iterations = 30
	rnd := rand.New(rand.NewSource(30052))
	turns := make([]logicalTurn, iterations)
	for n := range turns {
		turns[n] = randomTurn(rnd, n)
	}
	requireVariety(t, turns)
	for n, turn := range turns {
		script := []logicalTurn{turn}
		if slices.ContainsFunc(turn.blocks, func(b block) bool { return b.kind == blockToolCall }) {
			// The loop answers the calls and asks again; a text turn ends it.
			script = append(script, oneText("done"))
		}
		c := conformanceCase{
			name:   fmt.Sprintf("random-%d", n),
			prompt: "go",
			turns:  func(providerInfo) []logicalTurn { return script },
		}
		t.Run(c.name, func(t *testing.T) {
			for _, p := range providerTable {
				t.Run(p.name, func(t *testing.T) {
					t.Parallel()
					r := runCase(t, p, c, runOptions{})
					want, err := fauxSkeleton(turn)
					if err != nil {
						t.Fatal(err)
					}
					if got := skeleton(providerEvents(r.events, 0)); !slices.Equal(got, want) {
						t.Errorf("%s: blocks %s end %d: skeleton\n  %v\nwant\n  %v", r.name, describe(turn), turn.end, got, want)
					}
				})
			}
		})
	}
}

// describe lists the kinds of the blocks of t, for a failure message.
func describe(t logicalTurn) string {
	var kinds []string
	for _, b := range t.blocks {
		kinds = append(kinds, [...]string{"text", "thinking", "tool"}[b.kind])
	}
	return strings.Join(kinds, ",")
}

// requireVariety fails when the generated turns do not exercise what the
// property is about: every kind of block, several tool calls in one turn, and
// both ways a turn can end.
func requireVariety(t *testing.T, turns []logicalTurn) {
	t.Helper()
	var kinds [3]int
	var severalCalls, toolStops, plainStops int
	for _, turn := range turns {
		calls := 0
		for _, b := range turn.blocks {
			kinds[b.kind]++
			if b.kind == blockToolCall {
				calls++
			}
		}
		if calls > 1 {
			severalCalls++
		}
		if turn.end == endToolStop {
			toolStops++
		} else {
			plainStops++
		}
	}
	for kind, n := range kinds {
		if n < 5 {
			t.Errorf("the generator made %d blocks of kind %d in %d turns, want at least 5", n, kind, len(turns))
		}
	}
	if severalCalls == 0 || toolStops == 0 || plainStops == 0 {
		t.Errorf("the generator made %d turns with several tool calls, %d with a tool-call stop and %d with the plain stop, want some of each", severalCalls, toolStops, plainStops)
	}
}
