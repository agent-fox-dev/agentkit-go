package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// modulePath is the root module, and the import path of its root package.
const modulePath = "github.com/agent-fox-dev/agentkit-go"

// toolsOnly are the packages a program needs to run tools and code mode and
// to serve them over MCP, by the module directory they are listed from.
var toolsOnly = []struct {
	dir  string
	pkgs []string
}{
	{".", []string{"./runner", "./codemode", "./core", "./schema", "./guard"}},
	{"mcpserve", []string{"."}},
}

// forbidden reports whether a tools-only program must not link the package:
// the tree-sitter grammars, which are C; the root package and the providers,
// which bring the model loop; and the providers' SDKs with the cloud
// credential modules behind them. golang.org/x/oauth2 is not among them: the
// MCP client uses it. See
// docs/adr/10-keep-the-runner-and-the-output-buffer-free-of-cgo-and-providers.md.
func forbidden(path string) bool {
	if strings.Contains(path, "tree-sitter") || path == modulePath {
		return true
	}
	for _, p := range []string{
		modulePath + "/provider",
		"github.com/anthropics/anthropic-sdk-go",
		"github.com/OpenRouterTeam/go-sdk",
		"github.com/ollama/ollama",
	} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "github.com/aws/") || strings.HasPrefix(path, "cloud.google.com/")
}

// depGraph is what `go list -deps` reported: the packages asked for, and for
// every package listed the packages it imports.
type depGraph struct {
	roots   []string
	imports map[string][]string
}

// listDeps lists pkgs and everything they import, in the module at dir, with
// cgo enabled: with cgo off the tree-sitter imports sit behind a build
// constraint and no package would be seen to import them. A command that
// fails, or that lists nothing, is an error and never an empty graph.
func listDeps(dir string, pkgs ...string) (depGraph, error) {
	args := append([]string{"list", "-deps", "-json=ImportPath,Imports,DepOnly"}, pkgs...)
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	fail := func(err error) (depGraph, error) {
		return depGraph{}, fmt.Errorf("go %s in %s: %w\n%s", strings.Join(args, " "), dir, err, strings.TrimSpace(stderr.String()))
	}
	if err := cmd.Run(); err != nil {
		return fail(err)
	}
	g := depGraph{imports: map[string][]string{}}
	for dec := json.NewDecoder(&stdout); ; {
		var p struct {
			ImportPath string
			Imports    []string
			DepOnly    bool
		}
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fail(err)
		}
		g.imports[p.ImportPath] = p.Imports
		if !p.DepOnly {
			g.roots = append(g.roots, p.ImportPath)
		}
	}
	if len(g.roots) == 0 {
		return fail(errors.New("listed no package"))
	}
	return g, nil
}

// violation is a forbidden package one of the listed packages depends on,
// with the shortest chain of imports that leads to it.
type violation struct {
	root, forbidden string
	chain           []string
}

// importer is the package that brought the forbidden one in: the link before
// it in the chain.
func (v violation) importer() string { return v.chain[len(v.chain)-2] }

func (v violation) String() string {
	return fmt.Sprintf("%s depends on %s: %s", v.root, v.forbidden, strings.Join(v.chain, " -> "))
}

// violations walks the graph breadth-first from each root and reports every
// forbidden package it reaches through allowed ones, once per root, by its
// shortest chain. It does not walk on from a forbidden package: what that one
// imports came in with it.
func violations(g depGraph) []violation {
	var out []violation
	for _, root := range g.roots {
		from := map[string]string{root: ""}
		for queue := []string{root}; len(queue) > 0; queue = queue[1:] {
			pkg := queue[0]
			for _, imp := range g.imports[pkg] {
				if _, seen := from[imp]; seen {
					continue
				}
				from[imp] = pkg
				if !forbidden(imp) {
					queue = append(queue, imp)
					continue
				}
				chain := []string{imp}
				for p := pkg; p != ""; p = from[p] {
					chain = append(chain, p)
				}
				slices.Reverse(chain)
				out = append(out, violation{root: root, forbidden: imp, chain: chain})
			}
		}
	}
	return out
}

// A program that only runs tools must be able to link the runner, code mode
// and the MCP server without the tree-sitter grammars and without a model
// provider's SDK. Nothing else notices when one new import undoes that, so
// this gate runs with the short tests, in make check. It also builds the
// packages with cgo off.
func TestToolsOnlyPackagesLinkNoGrammarAndNoProvider(t *testing.T) {
	root := testutil.RepoRoot(t)
	for _, m := range toolsOnly {
		dir := filepath.Join(root, m.dir)
		g, err := listDeps(dir, m.pkgs...)
		if err != nil {
			t.Error(err)
			continue
		}
		for _, v := range violations(g) {
			t.Error(v)
		}
		cmd := exec.Command("go", append([]string{"build"}, m.pkgs...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go build %s in %s with CGO_ENABLED=0: %v\n%s", strings.Join(m.pkgs, " "), dir, err, strings.TrimSpace(string(out)))
		}
	}
}

// The gate is only worth having if it fires. These are packages of this
// repository that do link what it forbids, and for each it must name the
// forbidden package and the package that imports it.
func TestTheDependencyGateNamesWhatBroughtAForbiddenPackageIn(t *testing.T) {
	root := testutil.RepoRoot(t)
	for _, c := range []struct {
		name, pkg, root   string
		isForbidden       func(string) bool
		importer, example string
	}{
		{"the source tools link the grammars through outline", "./tools", modulePath + "/tools",
			func(p string) bool { return strings.Contains(p, "tree-sitter") }, modulePath + "/outline", "github.com/tree-sitter/go-tree-sitter"},
		{"the root package links the providers itself", ".", modulePath,
			func(p string) bool { return strings.HasPrefix(p, modulePath+"/provider/") }, modulePath, modulePath + "/provider/anthropic"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, err := listDeps(root, c.pkg)
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]int{}
			for _, v := range violations(g) {
				if seen[v.forbidden]++; !c.isForbidden(v.forbidden) {
					continue
				}
				want := c.root + " depends on " + v.forbidden + ": " + c.root + " -> "
				if v.importer() != c.importer || !strings.HasPrefix(v.String(), want) || !strings.HasSuffix(v.String(), " -> "+v.forbidden) {
					t.Errorf("violation %q, want it to start %q and to name %s as the importer", v, want, c.importer)
				}
			}
			if seen[c.example] != 1 {
				t.Errorf("%s reported %d times, want once; all: %v", c.example, seen[c.example], seen)
			}
			for p, n := range seen {
				if n != 1 {
					t.Errorf("%s reported %d times, want once per package", p, n)
				}
			}
		})
	}
}

// A package of the list that was renamed or removed must fail the gate, not
// pass it with nothing checked.
func TestTheDependencyGateFailsWhenItCannotListAPackage(t *testing.T) {
	root := testutil.RepoRoot(t)
	for name, c := range map[string]struct{ pkg, want string }{
		"a package that does not exist":     {"./nosuchpackage", "nosuchpackage"},
		"a pattern that matches no package": {"./docs/...", "listed no package"},
	} {
		g, err := listDeps(root, c.pkg)
		if err == nil || len(g.roots) != 0 || len(g.imports) != 0 {
			t.Errorf("%s: listDeps = %v, %v; want an error and no graph", name, g, err)
			continue
		}
		if msg := err.Error(); !strings.HasPrefix(msg, "go list -deps ") || !strings.Contains(msg, " in "+root+": ") || !strings.Contains(msg, c.want) {
			t.Errorf("%s: error %q, want the command, the directory and %q", name, msg, c.want)
		}
	}
}
