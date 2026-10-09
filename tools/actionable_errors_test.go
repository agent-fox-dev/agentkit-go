package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// schemaProp returns one property of a tool's input schema as marshalled for
// the provider.
func schemaProp(t *testing.T, tool coreTool, name string) map[string]any {
	t.Helper()
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	p, ok := s.Properties[name]
	if !ok {
		t.Fatalf("schema has no property %q: %s", name, b)
	}
	return p
}

// The limits are in the schema the model is given, not only in a runtime
// check it finds out about by hitting it: a model that is told the maximum, and
// whose provider enforces the schema, cannot ask for more.
func TestSearchSchemaAdvertisesItsLimits(t *testing.T) {
	tool := searchTool(t, searchTree(t))

	cl := schemaProp(t, tool, "context_lines")
	if cl["minimum"] != float64(0) || cl["maximum"] != float64(tools.MaxSearchContextLines) {
		t.Errorf("context_lines minimum/maximum = %v/%v, want 0/%d", cl["minimum"], cl["maximum"], tools.MaxSearchContextLines)
	}
	if d, _ := cl["description"].(string); !strings.Contains(d, "20") {
		t.Errorf("context_lines description %q does not state the cap of 20", d)
	}

	mm := schemaProp(t, tool, "max_matches")
	if mm["minimum"] != float64(1) || mm["maximum"] != float64(tools.SearchMatchCap) {
		t.Errorf("max_matches minimum/maximum = %v/%v, want 1/%d", mm["minimum"], mm["maximum"], tools.SearchMatchCap)
	}
	if d, _ := mm["description"].(string); !strings.Contains(d, "100") {
		t.Errorf("max_matches description %q does not state the cap of 100", d)
	}
}

// An out-of-range context_lines is still invalid_arguments, and now says what
// was received and what to send instead.
func TestContextLinesErrorEchoesTheValueAndSaysWhatToDo(t *testing.T) {
	tool := searchTool(t, searchTree(t))
	for _, tc := range []struct{ arg, got string }{{"25", "25"}, {"500", "500"}, {"-1", "-1"}} {
		res := tool.Execute(context.Background(),
			json.RawMessage(`{"pattern":"needle","context_lines":`+tc.arg+`}`))
		if res.OK || res.Error != "invalid_arguments" {
			t.Fatalf("context_lines %s: got %+v, want invalid_arguments", tc.arg, res)
		}
		if !strings.Contains(res.Detail, tc.got) {
			t.Errorf("context_lines %s: detail %q does not echo the value", tc.arg, res.Detail)
		}
		if !strings.Contains(res.Detail, "context_lines=20") {
			t.Errorf("context_lines %s: detail %q does not say to retry with context_lines=20 or less", tc.arg, res.Detail)
		}
	}
}

// A bad pattern says the pattern is a regular expression and how to write a
// literal metacharacter.
func TestPatternErrorHintsAtEscaping(t *testing.T) {
	tool := searchTool(t, searchTree(t))
	res := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"foo("}`))
	if res.OK || res.Error != "invalid_arguments" {
		t.Fatalf("got %+v, want invalid_arguments", res)
	}
	if !strings.Contains(res.Detail, "invalid pattern") {
		t.Errorf("detail %q lost the parser's own message", res.Detail)
	}
	if !strings.Contains(res.Detail, "regular expression") || !strings.Contains(res.Detail, `\(`) {
		t.Errorf("detail %q does not say the pattern is a regular expression and how to escape a literal (", res.Detail)
	}
}

// path_not_allowed names the root, as it did, and now says what to do next.
func TestPathNotAllowedSaysWhatToDoNext(t *testing.T) {
	root := searchTree(t)
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ws.Resolve("../..")
	if err == nil {
		t.Fatal("Resolve of a path outside the workspace succeeded")
	}
	if !strings.Contains(err.Error(), ws.Root) {
		t.Errorf("error %q does not name the workspace root %s", err, ws.Root)
	}
	if !strings.Contains(err.Error(), "inside the workspace") {
		t.Errorf("error %q does not say to use a path inside the workspace", err)
	}

	// And through the tools that report it.
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"search_files", "list_files", "find_files", "read_file"} {
		var tool coreTool
		for _, tl := range all {
			if tl.Name == name {
				tool = tl
			}
		}
		if tool.Name == "" {
			t.Fatalf("no tool %q", name)
		}
		args := `{"path":"../.."}`
		if name == "search_files" {
			args = `{"pattern":"needle","path":"../.."}`
		}
		res := tool.Execute(context.Background(), json.RawMessage(args))
		if res.OK || res.Error != "path_not_allowed" {
			t.Fatalf("%s: got %+v, want path_not_allowed", name, res)
		}
		if !strings.Contains(res.Detail, ws.Root) || !strings.Contains(res.Detail, "inside the workspace") {
			t.Errorf("%s: detail %q does not name the root and the next step", name, res.Detail)
		}
	}
}
