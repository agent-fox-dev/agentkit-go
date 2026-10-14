package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The sample stands in for the "test repo" of the real run: a Go module with
// one stub function and a version 2 spec package whose one task asks for the
// function to be implemented. The offline run syncs it into the sandbox in
// place of a repository, and puts a stand-in specr on the sandbox's PATH that
// prints the envelope the real one prints for this package.

const (
	sampleSpec = "01" // the package, as specr takes it: by number
	sampleTask = 1
)

// sampleProject is the sample's files: name, then content.
var sampleProject = [][2]string{
	{"go.mod", "module example.com/greeter\n\ngo 1.22\n"},
	{"README.md", "# greeter\n\nA sample project for AgentKit's harness example.\n"},
	{"greeter.go", "// Package greeter greets people.\npackage greeter\n\n// Greet greets name.\nfunc Greet(name string) string {\n\tpanic(\"not implemented\")\n}\n"},
	{".specs/01_greeting/prd.md", `---
spec_id: "01"
spec_name: "greeting"
title: "Greet a caller by name"
status: "active"
created_at: "2026-10-14T08:00:00Z"
updated_at: "2026-10-14T08:00:00Z"
intent_hash: "0000000000000000000000000000000000000000000000000000000000000000"
schema_version: 2
---
## Intent

The greeter package has a Greet function that nobody has written. Greet takes
a name and returns a greeting: "Hello, <name>!" for a name, and
"Hello, stranger!" when the name is empty, so a caller never greets nobody.

## Design Decisions

- D-1: the stranger case is a result, not an error: a greeting is never
  something a caller has to handle.
`},
	{".specs/01_greeting/requirements.json", `{
  "$schema": "https://agent-fox.dev/schemas/requirements.v2.json",
  "spec_id": "01",
  "spec_name": "greeting",
  "schema_version": 2,
  "introduction": "Greet returns a greeting for a name, and one for nobody.",
  "glossary": {"greeting": "The string Greet returns."},
  "requirements": [
    {
      "id": "01-REQ-1",
      "title": "Greet a caller",
      "criteria": [
        {"id": "01-REQ-1.1", "pattern": "ubiquitous", "system": "greeter", "action": "return \"Hello, <name>!\" for a non-empty name"},
        {"id": "01-REQ-1.2", "pattern": "unwanted", "system": "greeter", "trigger": "the name is empty", "action": "return \"Hello, stranger!\"", "contract": "the result is exactly \"Hello, stranger!\""}
      ]
    }
  ],
  "execution_paths": [
    {"id": "01-PATH-1", "title": "A caller greets", "steps": [
      {"actor": "caller", "action": "calls Greet(name)"},
      {"actor": "greeter", "action": "returns the greeting for name, or for a stranger"}
    ]}
  ],
  "external_apis": []
}
`},
	{".specs/01_greeting/test_spec.json", `{
  "$schema": "https://agent-fox.dev/schemas/test_spec.v2.json",
  "spec_id": "01",
  "spec_name": "greeting",
  "schema_version": 2,
  "tests": [
    {
      "id": "TS-01-1",
      "kind": "unit",
      "verifies": ["01-REQ-1.1", "01-REQ-1.2", "01-PATH-1"],
      "title": "Greet names the caller, and a stranger when there is no name",
      "given": ["the greeter package"],
      "when": "Greet is called with \"Ada\" and with \"\"",
      "then": ["Greet(\"Ada\") is \"Hello, Ada!\"", "Greet(\"\") is \"Hello, stranger!\""],
      "pseudocode": "for name, want in {Ada: Hello, Ada!, \"\": Hello, stranger!}: assert Greet(name) == want"
    }
  ]
}
`},
	{".specs/01_greeting/tasks.json", `{
  "$schema": "https://agent-fox.dev/schemas/tasks.v2.json",
  "spec_id": "01",
  "spec_name": "greeting",
  "schema_version": 2,
  "test_commands": {"linter": "gofmt -l .", "all_tests": "go test ./..."},
  "dependencies": [],
  "tasks": [
    {
      "id": 1,
      "kind": "integration",
      "title": "Implement Greet",
      "criteria": ["01-REQ-1.1", "01-REQ-1.2"],
      "tests": ["TS-01-1"],
      "steps": [
        "Write TS-01-1 in greeter_test.go and confirm it fails",
        "Implement Greet in greeter.go: the name when there is one, stranger when there is not",
        "Run gofmt -l . and go test ./..."
      ],
      "touches": ["greeter.go", "greeter_test.go"],
      "done_when": ["TS-01-1 passes", "gofmt -l . prints nothing"],
      "state": "pending"
    }
  ]
}
`},
}

// sampleRender is the Markdown the real specr renders for
// `specr 01 --task 1` on the sample package, abridged. It is what the
// stand-in prints as result.markdown.
const sampleRender = "> The package `01_greeting`, scoped to task 1 of 1.\n\n" +
	"# PRD\n\n- spec_id: 01\n- spec_name: greeting\n- title: Greet a caller by name\n- status: active\n\n" +
	"## Intent\n\nThe greeter package has a Greet function that nobody has written. Greet takes a name and returns a greeting: \"Hello, <name>!\" for a name, and \"Hello, stranger!\" when the name is empty, so a caller never greets nobody.\n\n" +
	"## Design Decisions\n\n- D-1: the stranger case is a result, not an error: a greeting is never something a caller has to handle.\n\n" +
	"# Requirements\n\nGreet returns a greeting for a name, and one for nobody.\n\n" +
	"## 01-REQ-1: Greet a caller\n\n- **01-REQ-1.1** (ubiquitous): the greeter shall return \"Hello, <name>!\" for a non-empty name.\n" +
	"- **01-REQ-1.2** (unwanted): if the name is empty, then the greeter shall return \"Hello, stranger!\". Contract: the result is exactly \"Hello, stranger!\".\n\n" +
	"### Execution paths\n\n- **01-PATH-1** A caller greets: caller calls Greet(name); greeter returns the greeting for name, or for a stranger.\n\n" +
	"# Test Specification\n\n## TS-01-1 (unit): Greet names the caller, and a stranger when there is no name\n\n- verifies: 01-REQ-1.1, 01-REQ-1.2, 01-PATH-1\n- given: the greeter package\n- when: Greet is called with \"Ada\" and with \"\"\n- then: Greet(\"Ada\") is \"Hello, Ada!\"; Greet(\"\") is \"Hello, stranger!\"\n- pseudocode: for name, want in {Ada: Hello, Ada!, \"\": Hello, stranger!}: assert Greet(name) == want\n\n" +
	"# Tasks\n\n- test_commands: linter `gofmt -l .`, all_tests `go test ./...`\n\n" +
	"## Task 1 (integration, pending): Implement Greet\n\n- criteria: 01-REQ-1.1, 01-REQ-1.2\n- tests: TS-01-1\n- steps:\n  1. Write TS-01-1 in greeter_test.go and confirm it fails\n  2. Implement Greet in greeter.go: the name when there is one, stranger when there is not\n  3. Run gofmt -l . and go test ./...\n- touches: greeter.go, greeter_test.go\n- done_when: TS-01-1 passes; gofmt -l . prints nothing\n"

// writeSample writes the sample project under dir.
func writeSample(dir string) error {
	for _, f := range sampleProject {
		p := filepath.Join(dir, filepath.FromSlash(f[0]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f[1]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeStandInSpecr writes a stand-in specr into binDir: a shell script that
// prints, for any arguments, the envelope the real specr prints for the
// sample package scoped to its one task. The real command renders the
// package it is pointed at; the stand-in exists so the offline run needs
// nothing installed.
func writeStandInSpecr(binDir string) error {
	env := map[string]any{
		"tool": "specr", "version": "stand-in", "ok": true, "status": "done", "exit_code": 0,
		"summary": "specr: rendered 01_greeting scoped to task 1 of 1 (prd, requirements, test_spec, tasks, ~" +
			fmt.Sprint(len(sampleRender)/4) + " tokens)",
		"result": map[string]any{
			"spec_dir": ".specs/01_greeting", "spec_id": "01", "spec_name": "greeting",
			"title": "Greet a caller by name", "status": "active", "valid": true, "scope": "task",
			"task":     map[string]any{"id": 1, "title": "Implement Greet", "kind": "integration", "state": "pending"},
			"sections": []string{"prd", "requirements", "test_spec", "tasks"},
			"tokens":   len(sampleRender) / 4,
			"tasks":    []map[string]any{{"id": 1, "title": "Implement Greet", "kind": "integration", "state": "pending"}},
			"markdown": sampleRender,
		},
		"next": []map[string]any{{"tool": "specr", "input": ".specs/01_greeting", "flags": []string{"--task", "1"},
			"why": "task 1 (Implement Greet) is the next task not done"}},
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if strings.Contains(string(body), "EOF") {
		return fmt.Errorf("the stand-in envelope holds the heredoc delimiter")
	}
	script := "#!/bin/sh\n# A stand-in for specr (github.com/agent-fox-dev/spec): the envelope the real\n" +
		"# command prints for `specr 01 --task 1` on the sample package.\ncat <<'EOF'\n" + string(body) + "\nEOF\n"
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(binDir, "specr"), []byte(script), 0o755)
}
