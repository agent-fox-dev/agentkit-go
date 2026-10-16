package core_test

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// toolSet builds tools whose Description is "<name> tool", in the order given.
func toolSet(names ...string) []core.Tool {
	ts := make([]core.Tool, len(names))
	for i, n := range names {
		ts[i] = core.Tool{Name: n, Description: n + " tool"}
	}
	return ts
}

func toolNames(ts []core.Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// TS-22-1: Named returns the requested tools in names order, matches only
// top-level tools and prefers the first of two same-named tools.
// 22-REQ-1.1.
func TestNamedPicksInNamesOrder_TS22_1(t *testing.T) {
	ts := toolSet("read_file", "write_file", "list_files")
	ts = append(ts, core.Tool{Name: "wrapper", ReachableTools: []core.Tool{{Name: "hidden"}}})
	got, err := core.Named(ts, "list_files", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list_files", "read_file"}; !reflect.DeepEqual(toolNames(got), want) {
		t.Fatalf("Named = %v, want %v", toolNames(got), want)
	}

	if got, err := core.Named(ts, "hidden"); got != nil || err == nil {
		t.Fatalf("Named(hidden) = %v, %v; want nil and an error: ReachableTools are not searched", got, err)
	}

	dup := []core.Tool{{Name: "dup", Description: "first"}, {Name: "dup", Description: "second"}}
	d, err := core.Named(dup, "dup")
	if err != nil || len(d) != 1 || d[0].Description != "first" {
		t.Fatalf("Named(dup) = %+v, %v; want the first dup", d, err)
	}
}

// TS-22-2: Named returns a fresh slice of value copies that does not alias ts.
// 22-REQ-1.2.
func TestNamedDoesNotAliasTheSet_TS22_2(t *testing.T) {
	ts := toolSet("read_file", "write_file", "list_files")
	orig := append([]core.Tool(nil), ts...)
	got, err := core.Named(ts, "read_file")
	if err != nil || len(got) != 1 {
		t.Fatalf("Named = %v, %v; want one tool", toolNames(got), err)
	}
	if &got[0] == &ts[0] {
		t.Fatal("the result shares ts's backing array")
	}
	got[0].Description = "changed"
	got = append(got, core.Tool{Name: "extra"})
	_ = got
	if !reflect.DeepEqual(ts, orig) {
		t.Fatalf("ts changed through the result:\n%+v\nwant\n%+v", ts, orig)
	}
}

// TS-22-3: Named with no names returns an empty non-nil slice and a nil
// error. 22-REQ-1.3.
func TestNamedWithNoNames_TS22_3(t *testing.T) {
	for name, ts := range map[string][]core.Tool{"non-empty": toolSet("read_file"), "nil": nil} {
		got, err := core.Named(ts)
		if got == nil || len(got) != 0 || err != nil {
			t.Errorf("%s set: Named() = %#v, %v; want an empty non-nil slice and no error", name, got, err)
		}
	}
}

// TS-22-4: Named reports every missing name and the names the set holds.
// 22-REQ-1.4.
func TestNamedReportsMissingNames_TS22_4(t *testing.T) {
	ts := toolSet("read_file", "write_file")

	got, err := core.Named(ts, "no_such_tool")
	if got != nil {
		t.Errorf("tools = %v, want nil", got)
	}
	if want := `core: no tool named "no_such_tool"; the set holds: read_file, write_file`; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}

	got, err = core.Named(ts, "write_file", "b_missing", "a_missing")
	if got != nil || err == nil {
		t.Fatalf("Named = %v, %v; want nil and an error", got, err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "core: ") {
		t.Errorf("error %q does not begin %q", msg, "core: ")
	}
	if b, a := strings.Index(msg, `"b_missing"`), strings.Index(msg, `"a_missing"`); b < 0 || a < 0 || b > a {
		t.Errorf("error %q does not quote b_missing before a_missing", msg)
	}
	if r, w := strings.Index(msg, "read_file"), strings.LastIndex(msg, "write_file"); r < 0 || w < 0 || r > w {
		t.Errorf("error %q does not list read_file before write_file", msg)
	}

	got, err = core.Named(nil, "x")
	if got != nil || err == nil {
		t.Fatalf("Named(empty) = %v, %v; want nil and an error", got, err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "core: ") || !strings.Contains(msg, `"x"`) || !strings.Contains(msg, "empty") {
		t.Errorf("error %q does not begin %q, quote x and say the set is empty", msg, "core: ")
	}
}

// TS-22-5: Named rejects a name repeated in names and quotes the first
// repeated name. 22-REQ-1.5.
func TestNamedRejectsRepeatedNames_TS22_5(t *testing.T) {
	ts := toolSet("read_file", "write_file", "list_files")
	got, err := core.Named(ts, "read_file", "write_file", "write_file", "read_file")
	if got != nil || err == nil {
		t.Fatalf("Named = %v, %v; want nil and an error", got, err)
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "core: ") || !strings.Contains(msg, `"write_file"`) {
		t.Errorf("error %q does not begin %q and quote write_file", msg, "core: ")
	}
	if strings.Contains(msg, `"read_file"`) {
		t.Errorf("error %q quotes read_file, whose repeat comes after write_file's", msg)
	}
}

// TS-22-6 (property): Named returns tools matching any sequence of distinct
// names from ts, and is race-free under concurrent calls. 22-REQ-1.6.
func TestNamedProperty_TS22_6(t *testing.T) {
	rng := rand.New(rand.NewPCG(22, 6))
	for range 200 {
		n := 1 + rng.IntN(12)
		all := make([]string, n)
		for i := range all {
			all[i] = fmt.Sprintf("tool_%d", i)
		}
		ts := toolSet(all...)
		picks := append([]string(nil), all...)
		rng.Shuffle(n, func(i, j int) { picks[i], picks[j] = picks[j], picks[i] })
		picks = picks[:rng.IntN(n+1)]

		got, err := core.Named(ts, picks...)
		if err != nil {
			t.Fatalf("Named(%v): %v", picks, err)
		}
		if len(picks) == 0 {
			picks = []string{}
		}
		if !reflect.DeepEqual(toolNames(got), picks) {
			t.Fatalf("Named = %v, want %v", toolNames(got), picks)
		}

		var wg sync.WaitGroup
		results := make([][]core.Tool, 8)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], _ = core.Named(ts, picks...)
			}()
		}
		wg.Wait()
		for i, r := range results {
			if !reflect.DeepEqual(r, got) {
				t.Fatalf("concurrent call %d = %v, want %v", i, toolNames(r), toolNames(got))
			}
		}
	}
}
