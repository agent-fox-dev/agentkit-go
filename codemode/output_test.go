package codemode_test

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/agent-fox-dev/agentkit-go/codemode"
)

// TS-08-33: printed lines are the result text.
func TestOutputPrintIsCaptured_TS08_33(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, "print('line 1')\nprint('line 2')\n")
	if !res.OK || res.Text != "line 1\nline 2" || res.Data["output"] != "line 1\nline 2" {
		t.Fatalf("result = %+v", res)
	}
}

// TS-08-34: main() wins over result, result over nothing.
func TestOutputReturnValuePrecedence_TS08_34(t *testing.T) {
	ctx := context.Background()
	if got := rv(t, run(t, ctx, codemode.Options{}, &fakeCaller{}, "def main():\n    return 'from_main'\nresult = 'from_result'\n")); got != `"from_main"` {
		t.Fatalf("main + result = %s", got)
	}
	if got := rv(t, run(t, ctx, codemode.Options{}, &fakeCaller{}, "result = 'from_result'\n")); got != `"from_result"` {
		t.Fatalf("result = %s", got)
	}
	if got := rv(t, run(t, ctx, codemode.Options{}, &fakeCaller{}, "x = 1\n")); got != `null` {
		t.Fatalf("neither = %s", got)
	}
}

// TS-08-35: the text is the output and the return value, either alone, or a
// fixed note when there is neither.
func TestOutputFormatResultText_TS08_35(t *testing.T) {
	r := rand.New(rand.NewSource(35))
	values := []any{nil, starlark.MakeInt(3), starlark.String("x"), starlark.NewList([]starlark.Value{starlark.True}), 42, "plain"}
	for range 100 {
		printed := []string{"", "hello", "a\nb"}[r.Intn(3)]
		v := values[r.Intn(len(values))]
		text := codemode.FormatResultText(printed, v)
		switch {
		case printed != "" && v != nil:
			if !strings.HasPrefix(text, printed+"\n") || !strings.Contains(text, "Return value: ") {
				t.Fatalf("both %q %v: %q", printed, v, text)
			}
		case printed != "":
			if text != printed {
				t.Fatalf("printed %q: %q", printed, text)
			}
		case v != nil:
			if !strings.HasPrefix(text, "Return value: ") {
				t.Fatalf("value %v: %q", v, text)
			}
		default:
			if text != "[Script finished with no output]" {
				t.Fatalf("neither: %q", text)
			}
		}
	}
	if got := codemode.FormatResultText("", starlark.None); got != "[Script finished with no output]" {
		t.Fatalf("None: %q", got)
	}
	if got := codemode.FormatResultText("", starlark.String("x")); got != `Return value: "x"` {
		t.Fatalf("string: %q", got)
	}
}

// TS-08-36: output past the limit keeps head and tail around a marker, and
// the whole output is in the spill file the marker and metadata name.
func TestOutputTruncatesAndSpills_TS08_36(t *testing.T) {
	spill := t.TempDir()
	res := run(t, context.Background(), codemode.Options{MaxOutputBytes: 100, SpillDir: spill}, &fakeCaller{},
		"print('HEAD' + 'X' * 500 + 'TAIL')\n")
	if res.Error != "output_limit_exceeded" || !strings.Contains(res.Text, "HEAD") || !strings.Contains(res.Text, "TAIL") ||
		!strings.Contains(res.Text, "bytes elided") {
		t.Fatalf("result = %+v", res)
	}
	if res.Metadata == nil || res.Metadata.SpillPath == "" || !strings.HasPrefix(res.Metadata.SpillPath, spill) ||
		!strings.Contains(res.Text, "Full output: "+res.Metadata.SpillPath) {
		t.Fatalf("metadata = %+v", res.Metadata)
	}
	data, err := os.ReadFile(res.Metadata.SpillPath)
	if err != nil || len(data) <= 500 || !strings.HasPrefix(string(data), "HEAD") || !strings.Contains(string(data), "TAIL") {
		t.Fatalf("spill file: %d bytes, %v", len(data), err)
	}
	// Output that fits leaves no spill file behind.
	quiet := t.TempDir()
	if res := run(t, context.Background(), codemode.Options{SpillDir: quiet}, &fakeCaller{}, "print('small')\n"); !res.OK {
		t.Fatalf("result = %+v", res)
	}
	if entries, _ := os.ReadDir(quiet); len(entries) != 0 {
		t.Fatalf("spill dir holds %d files after a run that fit", len(entries))
	}
}

// TS-08-37: with DisableSpill, the output is still truncated, and nothing is
// written to disk.
func TestOutputTruncatesWithoutSpill_TS08_37(t *testing.T) {
	spill := t.TempDir()
	res := run(t, context.Background(), codemode.Options{MaxOutputBytes: 100, SpillDir: spill, DisableSpill: true}, &fakeCaller{},
		"print('HEAD' + 'Y' * 500 + 'TAIL')\n")
	if !strings.Contains(res.Text, "bytes elided") || res.Metadata == nil || res.Metadata.SpillPath != "" ||
		strings.Contains(res.Text, "Full output:") {
		t.Fatalf("result = %+v / %+v", res, res.Metadata)
	}
	if entries, _ := os.ReadDir(spill); len(entries) != 0 {
		t.Fatalf("spill dir holds %d files with spilling disabled", len(entries))
	}
}

// TS-08-38: truncation is recorded in the result metadata.
func TestOutputTruncationMetadata_TS08_38(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{MaxOutputBytes: 100, SpillDir: t.TempDir()}, &fakeCaller{},
		"print('Z' * 400)\n")
	m := res.Metadata
	if m == nil || !m.Truncated || m.TruncatedBy != "bytes" || m.TotalBytes < 400 || m.DurationMS < 0 || m.SpillPath == "" {
		t.Fatalf("metadata = %+v", m)
	}
	// A run that fits is not marked truncated.
	ok := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, "print('fine')\n")
	if ok.Metadata != nil && ok.Metadata.Truncated {
		t.Fatalf("metadata = %+v", ok.Metadata)
	}
}
