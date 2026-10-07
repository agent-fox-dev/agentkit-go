package plugins_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/plugins"
)

func writeGo(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// REQ-SKILL-09: skill plugin code may not import an LLM client library or a
// model API package. A skill holding its own model client escapes the
// session's accounting, hooks and policy entirely.
func TestSkillLintFlagsModelClientImports(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "tools.go", `package p

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/agentfox/agentkit-go/provider"
)
`)
	bad, err := plugins.LintSkillImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 2 {
		t.Fatalf("violations = %+v, want the model SDK and the backend package", bad)
	}
	joined := bad[0].Import + "," + bad[1].Import
	if !strings.Contains(joined, "anthropic-sdk-go") || !strings.Contains(joined, "agentkit-go/provider") {
		t.Fatalf("imports = %q", joined)
	}
}

// The skill lint is a SUPERSET of REQ-PLUGIN-09's, not a replacement: the
// internal-package rule still applies to skill plugin code.
func TestSkillLintStillFlagsAgentkitInternals(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "x.go", `package p

import "github.com/agentfox/agentkit-go/internal/toml"
`)
	bad, err := plugins.LintSkillImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 1 {
		t.Fatalf("violations = %+v", bad)
	}
}

// The plugin lint must NOT gain the skill prefixes: REQ-PLUGIN-09 forbids
// agentkit internals and nothing else, and a plugin is allowed to be a
// backend — that is what BackendPlugin is for.
func TestThePluginLintIsUnchangedByTheSkillRules(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "x.go", `package p

import "github.com/anthropics/anthropic-sdk-go"
`)
	bad, err := plugins.LintImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("REQ-PLUGIN-09 flagged %+v; a backend plugin holds a model client by design", bad)
	}
}

// Prefixes match at a path-segment boundary, never as a bare substring.
func TestSkillLintMatchesWholePathSegments(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "x.go", `package p

import "github.com/openai/openai-go-community-fork/util"
`)
	bad, err := plugins.LintSkillImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 0 {
		t.Fatalf("flagged %+v; a different module that shares a prefix is not this rule's business", bad)
	}
}

// REQ-SEC-06 makes skill allowlist extensions ADDITIVE ONLY. The prohibited
// set must therefore not be reachable for deletion: the accessor hands out a
// copy, and mutating it cannot turn the check off.
func TestTheProhibitedSetCannotBeShortenedByACaller(t *testing.T) {
	got := plugins.SkillForbiddenImportPrefixes()
	if len(got) == 0 {
		t.Fatal("the prohibited set is empty")
	}
	for i := range got {
		got[i] = "example.com/harmless"
	}
	if again := plugins.SkillForbiddenImportPrefixes(); again[0] == "example.com/harmless" {
		t.Fatal("a caller mutated the package's own prohibited set")
	}
}

// A Go import path may be a RAW string literal. A lint that stripped only
// the surrounding double quotes left `...` uninspected — and therefore
// admitted — which is exactly the form somebody evading the lint would write.
func TestARawStringImportPathIsLintedLikeAQuotedOne(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "x.go", "package p\n\nimport `github.com/agentfox/agentkit-go/internal/toml`\n")
	bad, err := plugins.LintImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 1 || bad[0].Import != "github.com/agentfox/agentkit-go/internal/toml" || bad[0].Reason != "" {
		t.Fatalf("violations = %+v, want the backquoted internal import flagged", bad)
	}
	bad, err = plugins.LintSkillImports(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) != 1 {
		t.Fatalf("skill lint violations = %+v, want the same finding", bad)
	}
}

// Issue #84: a file whose import block does not parse is refused, not
// cleared — a syntax error in the imports is the cheapest way to hide one —
// and a forbidden import the partial parse can see is still named.
func TestAnUnparseableImportBlockIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "x.go", "package p\n\nimport (\n\t\"github.com/agentfox/agentkit-go/internal/secret\"\n\t\"fmt\"\n\nfunc F() { fmt.Println() }\n")
	for name, lint := range map[string]func(string) ([]plugins.BadImport, error){
		"LintImports": plugins.LintImports, "LintSkillImports": plugins.LintSkillImports,
	} {
		bad, err := lint(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var unreadable, internal bool
		for _, b := range bad {
			if strings.Contains(b.Reason, "could not parse") {
				unreadable = true
			}
			if b.Import == "github.com/agentfox/agentkit-go/internal/secret" && b.Reason == "" {
				internal = true
			}
		}
		if !unreadable {
			t.Errorf("%s: %+v; a file whose imports do not parse must be refused", name, bad)
		}
		if !internal {
			t.Errorf("%s: %+v; the internal import visible in the partial parse must be named", name, bad)
		}
	}
}
