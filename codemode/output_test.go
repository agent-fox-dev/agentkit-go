package codemode

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

func TestPrintedLinesAndTheReturnValueAreTheResultText(t *testing.T) {
	res := runScript(t, context.Background(), Options{}, caller(nil), "print('line 1')\nprint('line 2')\nresult = [1]\n")
	if !res.OK || res.Text != "line 1\nline 2\nReturn value: [1]" || res.Data["output"] != "line 1\nline 2" {
		t.Fatalf("result = %+v", res)
	}
	if res.Metadata == nil || res.Metadata.Truncated || res.Metadata.DurationMS < 0 {
		t.Fatalf("metadata = %+v", res.Metadata)
	}
}

// A Starlark value is written as Starlark writes it, any other value as JSON;
// with neither output nor a value the text says so.
func TestFormatResultText(t *testing.T) {
	for _, tc := range []struct {
		printed string
		value   any
		want    string
	}{
		{"hello", nil, "hello"},
		{"a\nb", starlark.MakeInt(3), "a\nb\nReturn value: 3"},
		{"", starlark.String("x"), `Return value: "x"`},
		{"", starlark.NewList([]starlark.Value{starlark.True}), "Return value: [True]"},
		{"", map[string]any{"k": 42}, `Return value: {"k":42}`},
		{"", nil, "[Script finished with no output]"},
		{"", starlark.None, "[Script finished with no output]"},
		{"", make(chan int), "[Script finished with no output]"},
	} {
		if got := FormatResultText(tc.printed, tc.value); got != tc.want {
			t.Errorf("FormatResultText(%q, %v) = %q, want %q", tc.printed, tc.value, got, tc.want)
		}
	}
}

// Output past the limit keeps its head and tail around a marker; the whole of
// it is in a spill file the marker and the metadata name, unless spilling is
// disabled; output that fits leaves no file behind.
func TestTruncatedOutputIsSpilledUnlessDisabled(t *testing.T) {
	for _, disable := range []bool{false, true} {
		spill := t.TempDir()
		res := runScript(t, context.Background(), Options{MaxOutputBytes: 100, SpillDir: spill, DisableSpill: disable},
			caller(nil), "print('HEAD' + 'X' * 500 + 'TAIL')\n")
		m := res.Metadata
		if res.Error != "output_limit_exceeded" || !strings.Contains(res.Text, "HEAD") || !strings.Contains(res.Text, "TAIL") ||
			!strings.Contains(res.Text, "bytes elided") || m == nil || !m.Truncated || m.TruncatedBy != "bytes" || m.TotalBytes < 500 {
			t.Fatalf("disable %v: result = %+v / %+v", disable, res, m)
		}
		entries, _ := os.ReadDir(spill)
		if disable {
			if m.SpillPath != "" || strings.Contains(res.Text, "Full output:") || len(entries) != 0 {
				t.Fatalf("spilled with spilling disabled: %+v, %d files", m, len(entries))
			}
			continue
		}
		if !strings.HasPrefix(m.SpillPath, spill) || !strings.Contains(res.Text, "Full output: "+m.SpillPath) {
			t.Fatalf("metadata = %+v, text %q", m, res.Text)
		}
		data, err := os.ReadFile(m.SpillPath)
		if err != nil || !strings.HasPrefix(string(data), "HEAD"+strings.Repeat("X", 500)+"TAIL") {
			t.Fatalf("spill file: %d bytes, %v", len(data), err)
		}
	}
	quiet := t.TempDir()
	if res := runScript(t, context.Background(), Options{SpillDir: quiet}, caller(nil), "print('small')\n"); !res.OK {
		t.Fatalf("result = %+v", res)
	}
	if entries, _ := os.ReadDir(quiet); len(entries) != 0 {
		t.Fatalf("spill dir holds %d files after a run that fit", len(entries))
	}
}
