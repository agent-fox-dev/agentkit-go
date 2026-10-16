package tools_test

// Spec 06 conformance: every built-in tool returns its documented error codes,
// and its Data conforms to its declared OutputSchema. The case table lives in
// internal/toolconformance, shared with the sandbox's proxy tools; here it
// runs against tools.For.
//
// code_search lives in its own module and is covered in codesearch/tool_test.go.

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/toolconformance"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// builtins writes tree to a fresh directory and returns tools.For over it,
// with no global git excludes.
func builtins(t *testing.T, tree toolconformance.Tree) []core.Tool {
	t.Helper()
	root := t.TempDir()
	tree.Write(t, root)
	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// TS-06-22: every built-in tool's successful Data conforms to its declared
// OutputSchema. 06-REQ-9.1.
func TestConformanceSuccessDataMatchesOutputSchema_TS06_22(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-22", builtins)
}

// TS-06-23: malformed JSON is invalid_arguments. 06-REQ-9.2.
func TestErrorCodesInvalidArguments_TS06_23(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-23", builtins)
}

// TS-06-24: a path outside the workspace is path_not_allowed. 06-REQ-9.3.
func TestErrorCodesPathNotAllowed_TS06_24(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-24", builtins)
}

// TS-06-25: a directory is not_a_file. 06-REQ-9.4.
func TestErrorCodesNotAFile_TS06_25(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-25", builtins)
}

// TS-06-26: a cancelled context is aborted. 06-REQ-9.5.
func TestErrorCodesAborted_TS06_26(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-26", builtins)
}

// TS-06-27: a failed subprocess still returns conforming Data. 06-REQ-9.6.
func TestConformanceSubprocessFailureData_TS06_27(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-27", builtins)
}

// TS-06-28: reading past the end of a file. 06-REQ-9.7.
func TestErrorCodesOffsetSchemeAddress_TS06_28(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-28", builtins)
}

// TS-06-29: the I/O failures each tool documents. 06-REQ-9.8.
func TestErrorCodesFailures_TS06_29(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-29", builtins)
}

// TS-06-30 (smoke, 06-PATH-1): the real read_file and list_files Data
// validates.
func TestSmokeBuiltinDataValidates_TS06_30(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-30", builtins)
}

// TS-06-33 (smoke, 06-PATH-4): the real execute and run_command exit 2 with
// conforming Data.
func TestSmokeSubprocessFailureData_TS06_33(t *testing.T) {
	toolconformance.RunGroup(t, "TS-06-33", builtins)
}
