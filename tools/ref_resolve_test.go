package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// TS-05-8 (unit): Declaration resolver queries shared symbol table for unqualified and container-qualified names
// Verifies: 05-REQ-2.1
func TestSymbolResolution_UnqualifiedAndQualified_TS_05_8(t *testing.T) {
	st := newSymbolTable()
	st.entries["runner.go"] = &symbolEntry{
		file: outline.File{
			Path: "runner.go",
			Decls: []outline.Decl{
				{
					Kind:      outline.KindType,
					Name:      "Runner",
					Exported:  true,
					StartLine: 3,
					EndLine:   5,
				},
				{
					Kind:      outline.KindMethod,
					Name:      "Run",
					Container: "Runner",
					Exported:  true,
					StartLine: 7,
					EndLine:   10,
				},
				{
					Kind:      outline.KindFunc,
					Name:      "Run",
					Exported:  true,
					StartLine: 12,
					EndLine:   15,
				},
			},
		},
	}

	matches := resolveSymbolCandidates(st, "Run", "", "")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for 'Run', got %d", len(matches))
	}

	m2 := resolveSymbolCandidates(st, "Runner.Run", "", "")
	if len(m2) != 1 {
		t.Fatalf("expected 1 match for 'Runner.Run', got %d", len(m2))
	}
	if m2[0].Container != "Runner" || m2[0].Name != "Run" {
		t.Fatalf("expected Runner.Run, got Container=%q Name=%q", m2[0].Container, m2[0].Name)
	}
}

// TS-05-9 (unit): Declaration resolver filters candidate declarations by kind and path constraints
// Verifies: 05-REQ-2.2
func TestSymbolResolution_KindAndPathFilter_TS_05_9(t *testing.T) {
	st := newSymbolTable()
	st.entries["pkg/config.go"] = &symbolEntry{
		file: outline.File{
			Path: "pkg/config.go",
			Decls: []outline.Decl{
				{
					Kind:      outline.KindType,
					Name:      "Config",
					Exported:  true,
					StartLine: 5,
					EndLine:   10,
				},
			},
		},
	}
	st.entries["pkg/setup.go"] = &symbolEntry{
		file: outline.File{
			Path: "pkg/setup.go",
			Decls: []outline.Decl{
				{
					Kind:      outline.KindFunc,
					Name:      "Config",
					Exported:  true,
					StartLine: 8,
					EndLine:   12,
				},
			},
		},
	}
	st.entries["cmd/main.go"] = &symbolEntry{
		file: outline.File{
			Path: "cmd/main.go",
			Decls: []outline.Decl{
				{
					Kind:      outline.KindType,
					Name:      "Config",
					Exported:  true,
					StartLine: 15,
					EndLine:   20,
				},
			},
		},
	}

	matches := resolveSymbolCandidates(st, "Config", "type", "pkg")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for 'Config' with kind 'type' in 'pkg', got %d", len(matches))
	}
	if matches[0].Path != "pkg/config.go" || matches[0].Kind != "type" {
		t.Fatalf("expected Path='pkg/config.go' and Kind='type', got Path=%q Kind=%q", matches[0].Path, matches[0].Kind)
	}
}

// TS-05-10 (unit): Declaration resolver selects the highest-ranked candidate applying find_symbol precedence
// Verifies: 05-REQ-2.3
func TestSymbolResolution_DisambiguationRanking_TS_05_10(t *testing.T) {
	candidates := []SymbolMatch{
		{
			Path:      "test/handle_test.go",
			Name:      "handle",
			Exported:  false,
			StartLine: 10,
		},
		{
			Path:      "a.go",
			Name:      "handle",
			Exported:  false,
			StartLine: 10,
		},
		{
			Path:      "test/handle_test.go",
			Name:      "Handle",
			Exported:  true,
			StartLine: 5,
		},
		{
			Path:      "pkg/handle.go",
			Name:      "Handle",
			Exported:  true,
			StartLine: 15,
		},
	}

	target := disambiguateSymbol(candidates, "Handle")
	if target.Path != "pkg/handle.go" || target.Name != "Handle" {
		t.Fatalf("expected top-ranked target Path='pkg/handle.go' and Name='Handle', got Path=%q Name=%q", target.Path, target.Name)
	}
}

// TS-05-11 (unit): Declaration resolver falls back to text search with zero target declaration when name is not found
// Verifies: 05-REQ-2.4
func TestSymbolResolution_FallbackTextSearch_TS_05_11(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	fr := newFileTools(Options{Workspace: ws}.withDefaults()).findReferencesTool()
	res := fr.Execute(context.Background(), []byte(`{"name":"GOPHER_KEY"}`))
	if !res.OK {
		t.Fatalf("expected res.OK == true, got error: %s (%s)", res.Error, res.Text)
	}

	data, ok := res.Data["result"].(ReferenceResult)
	if !ok {
		t.Fatalf("res.Data[\"result\"] is %T, want ReferenceResult", res.Data["result"])
	}
	if data.Target.Kind != "" || data.Target.StartLine != 0 {
		t.Fatalf("expected no declaration as target, got %+v", data.Target)
	}
	if data.Backend != "text" {
		t.Fatalf("expected backend 'text', got %q", data.Backend)
	}
	if !strings.Contains(res.Text, "0 references") {
		t.Fatalf("expected header to contain '0 references', got %q", res.Text)
	}
}

// TS-05-12 (property): Qualified declaration name splitting matches member and container across arbitrary dot-separated identifiers
// Verifies: 05-REQ-2.5
func TestSymbolResolution_SplitContainerMemberProperty_TS_05_12(t *testing.T) {
	st := newSymbolTable()

	testDecls := []struct {
		container string
		member    string
	}{
		{"Runner", "Run"},
		{"pkg.Service", "Start"},
		{"a.b.c.Manager", "Execute"},
		{"Deeply.Nested.Type", "Method"},
		{"Single", "Item"},
	}

	for i, td := range testDecls {
		path := fmt.Sprintf("file%d.go", i)
		st.entries[path] = &symbolEntry{
			file: outline.File{
				Path: path,
				Decls: []outline.Decl{
					{
						Kind:      outline.KindMethod,
						Name:      td.member,
						Container: td.container,
						Exported:  true,
						StartLine: 10 + i,
					},
				},
			},
		}
	}

	genQualifiedNames := func() []string {
		var names []string
		for _, td := range testDecls {
			names = append(names, td.container+"."+td.member)
		}
		// Additional names not in table to test nil match behavior.
		names = append(names, "NonExistent.Method", "Foo.Bar.Baz.Quux")
		return names
	}

	for _, name := range genQualifiedNames() {
		container, member := splitContainerMember(name)
		lastDot := strings.LastIndex(name, ".")
		if lastDot != -1 {
			expectedContainer := name[:lastDot]
			expectedMember := name[lastDot+1:]
			if container != expectedContainer {
				t.Fatalf("name %q: expected container %q, got %q", name, expectedContainer, container)
			}
			if member != expectedMember {
				t.Fatalf("name %q: expected member %q, got %q", name, expectedMember, member)
			}
		}

		for _, match := range resolveSymbolCandidates(st, name, "", "") {
			if match.Container != container || match.Name != member {
				t.Fatalf("match mismatch: expected Container=%q Name=%q, got Container=%q Name=%q",
					container, member, match.Container, match.Name)
			}
		}
	}
}

// TS-05-9 (unit): a path given as an absolute path inside the workspace
// scopes the declaration lookup as its relative form does.
// Verifies: 05-REQ-2.2
func TestSymbolResolution_AbsolutePathScope_TS05_9(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/p\n\ngo 1.22\n")
	writeFile(t, root, "svc/svc.go", "package svc\n\nfunc Handle() {}\n\nfunc call() { Handle() }\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{Workspace: ws}.withDefaults())
	args, _ := json.Marshal(map[string]string{"name": "Handle", "path": filepath.Join(ws.Root, "svc")})
	res := ft.findReferencesTool().Execute(context.Background(), args)
	if !res.OK {
		t.Fatalf("find_references: %s", res.Text)
	}
	if data := res.Data["result"].(ReferenceResult); data.Backend != "go/types" || data.Target.Name != "Handle" {
		t.Fatalf("Backend %q Target %+v, want go/types and Handle", data.Backend, data.Target)
	}
}
