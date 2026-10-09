package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// coreDecls parses the package's production files and returns the names of
// its declared types and the receiver types of each method name.
func coreDecls(t *testing.T) (types map[string]bool, methods map[string][]string) {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	types, methods = map[string]bool{}, map[string][]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok {
						types[ts.Name.Name] = true
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || len(d.Recv.List) == 0 {
					continue
				}
				recv := d.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if id, ok := recv.(*ast.Ident); ok {
					methods[d.Name.Name] = append(methods[d.Name.Name], id.Name)
				}
			}
		}
	}
	for _, rs := range methods {
		sort.Strings(rs)
	}
	return types, methods
}

func hasField(v any, name string) bool {
	_, ok := reflect.TypeOf(v).FieldByName(name)
	return ok
}

// TS-12-1: Message and ContentBlock are sealed to the canonical types, and
// the image, raw and tool-result blocks are gone.
func TestMessageAndBlockUnionsAreSealed_TS12_1(t *testing.T) {
	types, methods := coreDecls(t)
	if got := strings.Join(methods["isMessage"], ","); got != "AssistantMessage,ToolResultMessage,UserMessage" {
		t.Fatalf("Message implementors = %s", got)
	}
	if got := strings.Join(methods["isContentBlock"], ","); got != "TextBlock,ThinkingBlock,ToolUseBlock" {
		t.Fatalf("ContentBlock implementors = %s", got)
	}
	for _, gone := range []string{"ImageBlock", "RawBlock", "ToolResultBlock"} {
		if types[gone] {
			t.Errorf("core still declares %s", gone)
		}
	}
	// The seal is an unexported method: a type outside core cannot satisfy
	// the interfaces.
	for _, iface := range []reflect.Type{reflect.TypeOf((*Message)(nil)).Elem(), reflect.TypeOf((*ContentBlock)(nil)).Elem()} {
		sealed := false
		for i := 0; i < iface.NumMethod(); i++ {
			if iface.Method(i).PkgPath != "" {
				sealed = true
			}
		}
		if !sealed {
			t.Errorf("%s is not sealed", iface.Name())
		}
	}
}

// TS-12-2: the messages carry no jsonx, deferred or provenance fields beyond
// the model, and a tool use keeps its raw input.
func TestMessageFieldsArePruned_TS12_2(t *testing.T) {
	for _, m := range []any{UserMessage{}, AssistantMessage{}, ToolResultMessage{}} {
		for _, gone := range []string{"Unknown", "Deferred"} {
			if hasField(m, gone) {
				t.Errorf("%T still has %s", m, gone)
			}
		}
	}
	for _, gone := range []string{"ThinkingLevel", "API", "Provider"} {
		if hasField(AssistantMessage{}, gone) {
			t.Errorf("AssistantMessage still has %s", gone)
		}
	}
	if f, ok := reflect.TypeOf(AssistantMessage{}).FieldByName("Effort"); !ok || f.Type != reflect.TypeOf(Effort("")) {
		t.Error("AssistantMessage.Effort is missing or not an Effort")
	}
	for _, kept := range []string{"ID", "Name", "Input", "ThoughtSignature"} {
		if !hasField(ToolUseBlock{}, kept) {
			t.Errorf("ToolUseBlock lost %s", kept)
		}
	}
	if hasField(ToolUseBlock{}, "InputOrder") {
		t.Error("ToolUseBlock still has InputOrder")
	}
}
