package codemode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
)

// leaf is a bindable tool that does nothing.
func leaf(name string) core.Tool {
	return core.Tool{Name: name, Description: "the " + name + " tool",
		InputSchema: schema.Object(schema.Opt("v", schema.String())),
		Execute:     func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
}

// TS-08-1: New returns a tool with the configured name that reaches the
// bound tools and runs scripts.
func TestNewBuildsTheTool_TS08_1(t *testing.T) {
	tl, _, err := codemode.New([]core.Tool{leaf("dummy")}, codemode.Options{Name: "custom_mode"})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Name != "custom_mode" || len(tl.ReachableTools) != 1 || tl.ReachableTools[0].Name != "dummy" || tl.Execute == nil {
		t.Fatalf("tool = %+v", tl)
	}
	def, _, err := codemode.New([]core.Tool{leaf("dummy")}, codemode.Options{})
	if err != nil || def.Name != "code_mode" {
		t.Fatalf("default name = %q, %v", def.Name, err)
	}
}

// TS-08-2: the input schema requires a script; the output schema declares
// output, return_value and calls_completed.
func TestNewSchemas_TS08_2(t *testing.T) {
	tl, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := tl.InputSchema
	if in == nil || in.Properties["script"] == nil || in.Properties["script"].Type != schema.TypeString ||
		!in.IsRequired("script") || in.Properties["script"].Description == "" {
		t.Fatalf("InputSchema = %+v", in)
	}
	out := tl.OutputSchema
	if out == nil || out.Properties["output"] == nil || out.Properties["output"].Type != schema.TypeString ||
		out.Properties["calls_completed"] == nil || out.Properties["calls_completed"].Type != schema.TypeArray {
		t.Fatalf("OutputSchema = %+v", out)
	}
	if rv, ok := out.Properties["return_value"]; !ok || rv == nil || rv.Type != schema.TypeNone {
		t.Fatalf("return_value = %+v, want an untyped schema", rv)
	}
}

// TS-08-3: code mode cannot bind code mode — by name, or a code-mode tool
// under any name, directly or behind another wrapper.
func TestNewRefusesSelfNesting_TS08_3(t *testing.T) {
	const want = "codemode: cannot bind code_mode tool inside code_mode"
	if _, _, err := codemode.New([]core.Tool{{Name: "code_mode"}}, codemode.Options{Name: "code_mode"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("by name: err = %v", err)
	}
	inner, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{Name: "inner_scripts"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := codemode.New([]core.Tool{inner}, codemode.Options{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("existing code-mode tool: err = %v", err)
	}
	wrapper := leaf("wrapper")
	wrapper.ReachableTools = []core.Tool{inner}
	if _, _, err := codemode.New([]core.Tool{wrapper}, codemode.Options{}); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("behind a wrapper: err = %v", err)
	}
}

// TS-08-4: duplicate names are refused, naming the duplicate.
func TestNewRefusesDuplicateNames_TS08_4(t *testing.T) {
	_, _, err := codemode.New([]core.Tool{leaf("lookup"), leaf("lookup")}, codemode.Options{})
	if err == nil || !strings.Contains(err.Error(), "codemode: duplicate tool name: lookup") {
		t.Fatalf("err = %v", err)
	}
}

// TS-08-5: custom guidelines are used as given; without them the defaults
// are.
func TestNewGuidelines_TS08_5(t *testing.T) {
	t1, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{Guidelines: []string{"custom guide"}})
	if err != nil || len(t1.PromptGuidelines) != 1 || t1.PromptGuidelines[0] != "custom guide" {
		t.Fatalf("custom = %v, %v", t1.PromptGuidelines, err)
	}
	t2, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{})
	if err != nil || len(t2.PromptGuidelines) == 0 {
		t.Fatalf("default = %v, %v", t2.PromptGuidelines, err)
	}
}

// TS-08-6: BuildInfo reports the description's bytes and characters and the
// number of bound tools, for any tool set.
func TestBuildInfoMatchesDescription_TS08_6(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	for i := range 40 {
		n := r.Intn(6)
		ts := make([]core.Tool, n)
		for j := range ts {
			ts[j] = leaf(fmt.Sprintf("t%d_%d", i, j))
			ts[j].Description = strings.Repeat("é—x", r.Intn(5))
		}
		var opts codemode.Options
		if r.Intn(3) == 0 {
			opts.Description = strings.Repeat("ü", r.Intn(30))
		}
		tl, info, err := codemode.New(ts, opts)
		if err != nil {
			t.Fatal(err)
		}
		if info.BoundToolsCount != n || info.DescriptionBytes != len(tl.Description) ||
			info.DescriptionChars != utf8.RuneCountInString(tl.Description) {
			t.Fatalf("iteration %d: info %+v for a %d-byte, %d-char description and %d tools",
				i, info, len(tl.Description), utf8.RuneCountInString(tl.Description), n)
		}
	}
}
