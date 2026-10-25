package wire_test

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/wire"
)

func frames(t *testing.T, r *wire.FrameReader) []string {
	t.Helper()
	var out []string
	for {
		b, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("Next after %q: %v", out, err)
		}
		out = append(out, string(b))
	}
}

func TestNDJSONReadsOneFramePerLineAndEndsCleanly(t *testing.T) {
	r := wire.NewNDJSON(strings.NewReader("{\"a\":1}\n  \n\r\n{\"b\":2}\r\n{\"c\":3}"), wire.Limits{})
	if got := strings.Join(frames(t, r), "|"); got != `{"a":1}|{"b":2}|{"c":3}` {
		t.Fatalf("frames = %q", got)
	}
	if _, err := r.Next(); err != io.EOF || r.Poisoned() {
		t.Fatalf("after EOF: err = %v, poisoned = %v; a clean end must not poison", err, r.Poisoned())
	}
}

func TestNDJSONSkipsALongRunOfBlankLinesWithoutRecursing(t *testing.T) {
	const run = 12 << 20
	body := io.MultiReader(io.LimitReader(newlines{}, run), strings.NewReader(`{"a":1}`+"\n"))
	if got := frames(t, wire.NewNDJSON(body, wire.Limits{})); len(got) != 1 || got[0] != `{"a":1}` {
		t.Fatalf("frames = %q, want the frame after the blank run", got)
	}
}

func TestNDJSONBoundsAFrameAndARunOfBlankLines(t *testing.T) {
	const limit = 1024
	for name, body := range map[string]string{
		"oversized frame":     strings.Repeat("x", 5000) + "\n{}\n",
		"oversized blank run": strings.Repeat("\n", limit*4) + "{}\n",
	} {
		r := wire.NewNDJSON(strings.NewReader(body), wire.Limits{MaxMessageBytes: limit})
		_, err := r.Next()
		if we := wireErr(t, err); we.Rule != wire.RuleMessageBytes {
			t.Fatalf("%s: rule = %s, want message_bytes", name, we.Rule)
		}
		if _, again := r.Next(); !r.Poisoned() || again == nil || again.Error() != err.Error() {
			t.Fatalf("%s: a rejected reader resynchronized: first %v, then %v", name, err, again)
		}
	}
	// The blank-line count resets on every frame, so keep-alives never add up.
	body := strings.Repeat(strings.Repeat("\n", limit/2)+"{}\n", 8)
	if got := frames(t, wire.NewNDJSON(strings.NewReader(body), wire.Limits{MaxMessageBytes: limit})); len(got) != 8 {
		t.Fatalf("frames = %d, want 8", len(got))
	}
}

func TestContentLengthReadsTheDeclaredBodyAndIgnoresOtherHeaders(t *testing.T) {
	body := "Content-Type: x\r\ncontent-length:2\r\n\r\n{}" + "Content-Length: 3\n\n[1]"
	r := wire.NewContentLength(strings.NewReader(body), wire.Limits{})
	if got := strings.Join(frames(t, r), "|"); got != "{}|[1]" {
		t.Fatalf("frames = %q", got)
	}
	if r.Poisoned() {
		t.Fatal("a clean end must not poison the reader")
	}
}

func TestContentLengthRejectsAMalformedOrOversizedHeader(t *testing.T) {
	for _, tc := range []struct {
		name, frame string
		rule        wire.Rule
	}{
		{"2^31", "Content-Length: 2147483648\r\n\r\n", wire.RuleMessageBytes},
		{"2^32", "Content-Length: 4294967296\r\n\r\n", wire.RuleMessageBytes},
		{"2^63 overflows a signed parse", "Content-Length: 9223372036854775808\r\n\r\n", wire.RuleMessageBytes},
		{"2^64-1", "Content-Length: 18446744073709551615\r\n\r\n", wire.RuleMessageBytes},
		{"over the limit", "Content-Length: 300\r\n\r\n", wire.RuleMessageBytes},
		{"header line over 8 KiB", "X: " + strings.Repeat("a", 9<<10) + "\r\n\r\n", wire.RuleMessageBytes},
		{"negative", "Content-Length: -1\r\n\r\n{}", wire.RuleSyntax},
		{"empty", "Content-Length: \r\n\r\n", wire.RuleSyntax},
		{"no colon", "no colon\r\n\r\n", wire.RuleSyntax},
		{"no Content-Length", "Content-Type: x\r\n\r\n{}", wire.RuleSyntax},
		{"body shorter than declared", "Content-Length: 5\r\n\r\n{", wire.RuleSyntax},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := wire.NewContentLength(strings.NewReader(tc.frame), wire.Limits{MaxMessageBytes: 256})
			_, err := r.Next()
			if we := wireErr(t, err); we.Rule != tc.rule {
				t.Fatalf("rule = %s, want %s (%v)", we.Rule, tc.rule, err)
			}
			if !r.Poisoned() {
				t.Fatal("a rejection must poison the reader")
			}
		})
	}
}

func TestContentLengthDoesNotAllocateWhatTheHeaderDeclares(t *testing.T) {
	const declared = 16 << 20
	r := wire.NewContentLength(strings.NewReader(fmt.Sprintf("Content-Length: %d\r\n\r\n{", declared)), wire.Limits{})
	before := totalAlloc()
	_, err := r.Next()
	grew := totalAlloc() - before
	if err == nil {
		t.Fatal("a truncated frame must be refused")
	}
	if grew > declared/4 {
		t.Fatalf("a 1-byte body under a %d-byte declaration allocated %d bytes", declared, grew)
	}
}

func TestFrameReaderPassesThroughAReadError(t *testing.T) {
	r := wire.NewContentLength(io.MultiReader(strings.NewReader("Content-Length: 4\r\n\r\n{"), errReader{}), wire.Limits{})
	if _, err := r.Next(); err != errBoom || !r.Poisoned() {
		t.Fatalf("err = %v, poisoned = %v; want the stream's own error, poisoning", err, r.Poisoned())
	}
}

var errBoom = fmt.Errorf("boom")

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errBoom }

// FuzzNDJSONFrameReaderNeverPanics drives the newline framing with arbitrary bytes.
func FuzzNDJSONFrameReaderNeverPanics(f *testing.F) {
	for _, s := range []string{
		"", "\n", "\n\n", `{"a":1}` + "\n", `{"a":1}`, "\r\n", "  \n{}\n",
		`{"a":1}` + "\n" + `{"b":2}` + "\n", "\x00\n", strings.Repeat("x", 300) + "\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		drain(t, wire.NewNDJSON(bytes.NewReader(b), wire.Limits{MaxMessageBytes: 256}), b)
	})
}

// FuzzContentLengthFrameReaderNeverPanics drives the header framing, whose
// declared length is the input a peer controls.
func FuzzContentLengthFrameReaderNeverPanics(f *testing.F) {
	for _, s := range []string{
		"", "\r\n", "Content-Length: 2\r\n\r\n{}",
		"Content-Length: 2\r\n\r\n{}Content-Length: 3\r\n\r\n[1]",
		"content-length:0\r\n\r\n", "Content-Length: -1\r\n\r\n",
		"Content-Length: 18446744073709551615\r\n\r\n", "Content-Length: 9223372036854775808\r\n\r\n",
		"Content-Length: 5\r\n\r\n{", "Content-Type: x\r\n\r\n", "no colon\r\n\r\n",
		"Content-Length: 2\nContent-Length: 1\n\n{}", "Content-Length: 300\r\n\r\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		drain(t, wire.NewContentLength(bytes.NewReader(b), wire.Limits{MaxMessageBytes: 256}), b)
	})
}
