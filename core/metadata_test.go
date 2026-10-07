package core_test

import (
	"testing"

	"github.com/agentfox/agentkit-go/core"
)

// TS-04-39: ToolResultMessage.Clone deep-copies Metadata including the ExitCode pointer.
func TestToolResultMessageCloneDeepCopiesMetadata_TS04_39(t *testing.T) {
	three := 3
	orig := core.ToolResultMessage{
		ToolUseID: "c1",
		ToolName:  "probe",
		Content:   core.Content{core.TextBlock{Text: "hello"}},
		Metadata: &core.ToolMetadata{
			ExitCode:  &three,
			Outcome:   "exit",
			SpillPath: "/tmp/x.log",
		},
	}

	c := orig.Clone().(core.ToolResultMessage)

	// Pointers must differ.
	if c.Metadata == orig.Metadata {
		t.Fatal("Clone().Metadata must be a different pointer than the original")
	}
	if c.Metadata.ExitCode == orig.Metadata.ExitCode {
		t.Fatal("Clone().Metadata.ExitCode must be a different pointer than the original")
	}

	// Values must match before mutation.
	if *c.Metadata.ExitCode != 3 {
		t.Fatalf("clone ExitCode = %d, want 3", *c.Metadata.ExitCode)
	}
	if c.Metadata.Outcome != "exit" {
		t.Fatalf("clone Outcome = %q, want %q", c.Metadata.Outcome, "exit")
	}
	if c.Metadata.SpillPath != "/tmp/x.log" {
		t.Fatalf("clone SpillPath = %q, want %q", c.Metadata.SpillPath, "/tmp/x.log")
	}

	// Mutate the clone; original must be unchanged.
	c.Metadata.Outcome = "changed"
	*c.Metadata.ExitCode = 9

	if orig.Metadata.Outcome != "exit" {
		t.Fatalf("original Outcome changed to %q after clone mutation", orig.Metadata.Outcome)
	}
	if *orig.Metadata.ExitCode != 3 {
		t.Fatalf("original ExitCode changed to %d after clone mutation", *orig.Metadata.ExitCode)
	}

	// A nil-Metadata message clones to nil Metadata.
	nilMsg := core.ToolResultMessage{ToolUseID: "c2", ToolName: "x"}
	nilClone := nilMsg.Clone().(core.ToolResultMessage)
	if nilClone.Metadata != nil {
		t.Fatal("clone of nil-Metadata message should have nil Metadata")
	}
}

// TS-04-39 supplement: ToolMetadata.Clone and IsEmpty unit tests.
func TestToolMetadataCloneAndIsEmpty(t *testing.T) {
	// nil.Clone() == nil
	var nilMD *core.ToolMetadata
	if nilMD.Clone() != nil {
		t.Fatal("nil.Clone() must return nil")
	}
	if !nilMD.IsEmpty() {
		t.Fatal("nil.IsEmpty() must return true")
	}

	// All-zero is empty.
	zero := &core.ToolMetadata{}
	if !zero.IsEmpty() {
		t.Fatal("all-zero ToolMetadata should be empty")
	}

	// DurationMS: 0 is still empty.
	durZero := &core.ToolMetadata{DurationMS: 0}
	if !durZero.IsEmpty() {
		t.Fatal("ToolMetadata{DurationMS:0} should be empty")
	}

	// ExitCode: &0 is NOT empty.
	ec0 := 0
	withEC := &core.ToolMetadata{ExitCode: &ec0}
	if withEC.IsEmpty() {
		t.Fatal("ToolMetadata with non-nil ExitCode pointer should not be empty")
	}

	// A populated metadata is not empty.
	ec2 := 2
	full := &core.ToolMetadata{ExitCode: &ec2, Outcome: "exit", TotalBytes: 5}
	if full.IsEmpty() {
		t.Fatal("populated ToolMetadata should not be empty")
	}

	// Clone produces independent copy.
	c := full.Clone()
	if c == full {
		t.Fatal("Clone must return a different pointer")
	}
	if c.ExitCode == full.ExitCode {
		t.Fatal("Clone must produce a different ExitCode pointer")
	}
	if *c.ExitCode != 2 || c.Outcome != "exit" || c.TotalBytes != 5 {
		t.Fatal("Clone values do not match original")
	}
}
