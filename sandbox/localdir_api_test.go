package sandbox_test

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// fieldList is a struct type's fields as (name, type) pairs, in order.
func fieldList(t reflect.Type) [][2]string {
	var out [][2]string
	for i := range t.NumField() {
		out = append(out, [2]string{t.Field(i).Name, t.Field(i).Type.String()})
	}
	return out
}

// TS-14-1: LocalDir has the five specified fields and implements both sealed
// interfaces, SyncResult has Files, Bytes, Deleted and Sent, and no
// gokrazy/rsync type reaches the sandbox package's exported API.
// 14-REQ-1.1, 14-REQ-1.3.
func TestLocalDirAPI_TS14_1(t *testing.T) {
	want := [][2]string{{"Path", "string"}, {"Ignore", reflect.TypeFor[workspace.IgnoreOptions]().String()},
		{"IncludeIgnored", "bool"}, {"ExcludeGit", "bool"}, {"Delete", "bool"}}
	if got := fieldList(reflect.TypeFor[sandbox.LocalDir]()); !reflect.DeepEqual(got, want) {
		t.Errorf("LocalDir fields %v, want %v", got, want)
	}
	var _ sandbox.Source = sandbox.LocalDir{}
	var _ sandbox.Destination = sandbox.LocalDir{}
	var _ sandbox.Source = sandbox.Tar{}
	var _ sandbox.Destination = sandbox.TarOut{}
	wantRes := [][2]string{{"Files", "int"}, {"Bytes", "int64"}, {"Deleted", "int"}, {"Sent", "int64"}}
	if got := fieldList(reflect.TypeFor[sandbox.SyncResult]()); !reflect.DeepEqual(got, wantRes) {
		t.Errorf("SyncResult fields %v, want %v", got, wantRes)
	}

	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "github.com/gokrazy/rsync") {
				t.Errorf("%s imports %s", n, imp.Path.Value)
			}
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[sandbox.LocalDir](), reflect.TypeFor[sandbox.SyncResult]()} {
		for i := range typ.NumField() {
			if strings.HasPrefix(typ.Field(i).Type.PkgPath(), "github.com/gokrazy/rsync") {
				t.Errorf("%s.%s has a gokrazy/rsync type", typ.Name(), typ.Field(i).Name)
			}
		}
		for i := range typ.NumMethod() {
			m := typ.Method(i).Type
			for j := range m.NumIn() {
				if strings.HasPrefix(m.In(j).PkgPath(), "github.com/gokrazy/rsync") {
					t.Errorf("%s.%s takes a gokrazy/rsync type", typ.Name(), typ.Method(i).Name)
				}
			}
		}
	}
}
