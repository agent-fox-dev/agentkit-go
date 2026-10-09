package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/schema"
)

func toolUse(t *testing.T, id, name, args string) ToolUseBlock {
	t.Helper()
	b, err := NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("NewToolUse: %v", err)
	}
	return b
}

func TestPreparedArgumentsPreserveKeyOrder(t *testing.T) {
	tool := Tool{
		Name: "t", InputSchema: schema.Object(
			schema.Prop("zeta", schema.String()), schema.Opt("alpha", schema.String())),
	}
	c := toolUse(t, "c1", "t", `{"zeta":"1","alpha":"2"}`)
	p, err := PrepareArguments(tool, c)
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Raw) != `{"zeta":"1","alpha":"2"}` {
		t.Fatalf("Raw = %s; the model's own bytes must pass through untouched when "+
			"nothing changed (REQ-PROV-17)", p.Raw)
	}
}

func TestOptionalNullsAreDeletedNotRejected(t *testing.T) {
	// Constrained sampling forces the model to emit every declared property,
	// so optional fields arrive as explicit nulls.
	tool := Tool{
		Name: "t", InputSchema: schema.Object(
			schema.Prop("path", schema.String()), schema.Opt("limit", schema.Int())),
	}
	c := toolUse(t, "c1", "t", `{"path":"/x","limit":null}`)
	p, err := PrepareArguments(tool, c)
	if err != nil {
		t.Fatalf("an explicit null for an OPTIONAL property must be deleted, not rejected "+
			"(REQ-TOOL-11.2): %v", err)
	}
	if _, present := p.Args["limit"]; present {
		t.Fatal("the optional null was not deleted")
	}
}

func TestValidationErrorEchoesTheModelsOwnKeyOrder(t *testing.T) {
	tool := Tool{
		Name: "t", InputSchema: schema.Object(schema.Prop("path", schema.String())),
	}
	c := toolUse(t, "c1", "t", `{"zeta":1,"alpha":2}`)
	_, err := PrepareArguments(tool, c)
	if err == nil {
		t.Fatal("want a validation error for a missing required property")
	}
	msg := err.Error()
	zi, ai := strings.Index(msg, "zeta"), strings.Index(msg, "alpha")
	if zi < 0 || ai < 0 || zi > ai {
		t.Fatalf("error text did not echo the model's own key order (REQ-TOOL-12.3):\n%s", msg)
	}
}

// Issue #71: an integer written with trailing separators — `"1, "`, as a
// model wrote read_file's offset — is coerced, like a plain `"120"`.
func TestIntegersWithTrailingSeparatorsAreCoerced(t *testing.T) {
	tool := Tool{Name: "read_file", InputSchema: schema.Object(
		schema.Prop("path", schema.String()), schema.Opt("offset", schema.Int()), schema.Opt("limit", schema.Int()))}
	for in, want := range map[string]float64{`"1, "`: 1, `"120"`: 120, `" 7 "`: 7, `"30;"`: 30} {
		p, err := PrepareArguments(tool, toolUse(t, "c", "read_file", `{"path":"a.go","offset":`+in+`}`))
		if err != nil {
			t.Errorf("offset %s: %v", in, err)
			continue
		}
		if got := fmt.Sprint(p.Args["offset"]); got != fmt.Sprint(want) {
			t.Errorf("offset %s coerced to %v, want %v", in, p.Args["offset"], want)
		}
	}
}

// Issue #71: what cannot be coerced is refused in one line that quotes what
// was received and says what to send.
func TestAnUncoercibleIntegerGetsAOneLineHint(t *testing.T) {
	tool := Tool{Name: "read_file", InputSchema: schema.Object(
		schema.Prop("path", schema.String()), schema.Opt("offset", schema.Int()))}
	_, err := PrepareArguments(tool, toolUse(t, "c", "read_file", `{"path":"a.go","offset":"1-50"}`))
	if err == nil {
		t.Fatal(`offset "1-50" must be refused`)
	}
	if msg := err.Error(); !strings.Contains(msg, `"1-50"`) || !strings.Contains(msg, "a number") {
		t.Fatalf("message must quote the value and say to pass a number:\n%s", msg)
	}
}

// Issue #71: a validation failure echoes the arguments ONCE, in the model's
// own key order (REQ-TOOL-12.3), with long string values abbreviated. The
// failure being explained is a shape, and echoing a kilobyte of edit text
// back — twice — five times in a turn buried it.
func TestAValidationErrorEchoesOnceAndAbbreviatesLongStrings(t *testing.T) {
	tool := Tool{Name: "edit_file", InputSchema: schema.Object(
		schema.Prop("path", schema.String()),
		schema.Prop("edits", schema.Array(schema.Object(
			schema.Prop("old_string", schema.String()), schema.Prop("new_string", schema.String())))))}
	long := strings.Repeat("x", 1000)
	args, _ := json.Marshal(map[string]any{"path": "p.go", "edits": "[{\"old_string\">" + long})
	_, err := PrepareArguments(tool, toolUse(t, "c", "edit_file", string(args)))
	if err == nil {
		t.Fatal("want a validation error")
	}
	msg := err.Error()
	if n := strings.Count(msg, "p.go"); n != 1 {
		t.Errorf("the arguments are echoed %d times, want once:\n%s", n, msg)
	}
	if len(msg) > 600 {
		t.Errorf("the error is %d bytes for a 1 KB argument; long strings must be abbreviated:\n%s", len(msg), msg)
	}
	if !strings.Contains(msg, "not a JSON string") {
		t.Errorf("an array sent as a string must say so:\n%s", msg)
	}
}

// TS-12-3: PreparedArguments is the raw bytes, the decoded map and the
// coercions, with no ordered form.
func TestPreparedArgumentsFields_TS12_3(t *testing.T) {
	p := PreparedArguments{Raw: json.RawMessage(`{"k":"v"}`), Args: map[string]any{"k": "v"}}
	typ := reflect.TypeOf(p)
	want := map[string]reflect.Type{
		"Raw":       reflect.TypeOf(json.RawMessage(nil)),
		"Args":      reflect.TypeOf(map[string]any(nil)),
		"Coercions": reflect.TypeOf([]schema.Coercion(nil)),
	}
	if typ.NumField() != len(want) {
		t.Fatalf("PreparedArguments has %d fields, want %d", typ.NumField(), len(want))
	}
	for name, ft := range want {
		f, ok := typ.FieldByName(name)
		if !ok || f.Type != ft {
			t.Errorf("field %s missing or not %s", name, ft)
		}
	}
}

// TS-12-4: arguments nothing changed reach the handler as the model's own
// bytes.
func TestPrepareKeepsTheModelsBytes_TS12_4(t *testing.T) {
	in := json.RawMessage(`{"query": "test",  "limit":10}`)
	tool := Tool{Name: "search", InputSchema: schema.Object(schema.Prop("query", schema.String()), schema.Opt("limit", schema.Int()))}
	p, err := PrepareArguments(tool, ToolUseBlock{ID: "t1", Name: "search", Input: in})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Raw, in) {
		t.Fatalf("Raw = %s, want the input bytes %s", p.Raw, in)
	}
	if p.Args["query"] != "test" || p.Args["limit"] != json.Number("10") {
		t.Fatalf("Args = %v", p.Args)
	}
}

// TS-12-5: a tool's own argument repair is re-encoded into Raw.
func TestPrepareReencodesARepair_TS12_5(t *testing.T) {
	tool := Tool{Name: "custom", PrepareArguments: func(a map[string]any) map[string]any {
		a["injected"] = true
		return a
	}}
	p, err := PrepareArguments(tool, ToolUseBlock{ID: "t1", Name: "custom", Input: json.RawMessage(`{"v": 1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Raw) != `{"v": 1,"injected":true}` || p.Args["injected"] != true {
		t.Fatalf("Raw = %s, Args = %v; want the untouched key kept and the new one added", p.Raw, p.Args)
	}
}

// TS-12-6: arguments the schema rejects are an error and no arguments.
func TestPrepareRejectsInvalidArguments_TS12_6(t *testing.T) {
	tool := Tool{Name: "t", InputSchema: schema.Object(schema.Prop("count", schema.Int()))}
	p, err := PrepareArguments(tool, ToolUseBlock{ID: "t1", Name: "t", Input: json.RawMessage(`{"count":"invalid_string"}`)})
	if !errors.Is(err, schema.ErrArgumentValidation) || len(p.Raw) != 0 || p.Args != nil {
		t.Fatalf("PrepareArguments = %+v, %v; want nothing and a validation error", p, err)
	}
}
