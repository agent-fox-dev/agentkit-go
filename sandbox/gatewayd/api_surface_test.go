package gatewayd_test

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// exportedAPI lists the exported identifiers of the Go package in dir, one
// per line and sorted: types, functions, methods of exported types, variables
// and constants, and the exported fields of exported structs.
func exportedAPI(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var api []string
	fset := token.NewFileSet()
	for _, e := range entries {
		if n := e.Name(); !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					api = append(api, "func "+d.Name.Name)
					continue
				}
				recv := d.Recv.List[0].Type
				star := ""
				if s, ok := recv.(*ast.StarExpr); ok {
					recv, star = s.X, "*"
				}
				if id, ok := recv.(*ast.Ident); ok && id.IsExported() {
					api = append(api, "method ("+star+id.Name+")."+d.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						api = append(api, "type "+s.Name.Name)
						if st, ok := s.Type.(*ast.StructType); ok {
							for _, fl := range st.Fields.List {
								for _, n := range fl.Names {
									if n.IsExported() {
										api = append(api, "field "+s.Name.Name+"."+n.Name)
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								api = append(api, d.Tok.String()+" "+n.Name)
							}
						}
					}
				}
			}
		}
	}
	slices.Sort(api)
	return api
}

// TS-25-43: the gatewayd exported surface gains exactly five identifiers and
// the protocol constants are unchanged. 25-REQ-9.5.
func TestExportedSurfaceGainsFive_TS25_43(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "exported-api.spec19.golden"))
	if err != nil {
		t.Fatal(err)
	}
	golden := strings.Split(strings.TrimSpace(string(raw)), "\n")
	now := exportedAPI(t, ".")

	var added, removed []string
	for _, id := range now {
		if !slices.Contains(golden, id) {
			added = append(added, id)
		}
	}
	for _, id := range golden {
		if !slices.Contains(now, id) {
			removed = append(removed, id)
		}
	}
	want := []string{"field Config.Authenticate", "func PrincipalFromContext", "method (*Server).Destroy", "type Authenticator", "var ErrNotHeld"}
	if !slices.Equal(added, want) {
		t.Errorf("the surface gained %q, want exactly %q", added, want)
	}
	if len(removed) != 0 {
		t.Errorf("the surface lost %q", removed)
	}

	if sandbox.ProtocolMinor != 2 {
		t.Errorf("sandbox.ProtocolMinor = %d, want 2", sandbox.ProtocolMinor)
	}
	for _, name := range []string{"gateway.proto", "sandbox.proto"} {
		dir := filepath.Join("..", "internal", "sandboxpb")
		proto, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		recorded, err := os.ReadFile(filepath.Join(dir, name+".sha256"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(proto)
		if got := hex.EncodeToString(sum[:]); got != strings.TrimSpace(string(recorded)) {
			t.Errorf("%s hashes to %s, its recorded hash is %s", name, got, strings.TrimSpace(string(recorded)))
		}
	}
}
