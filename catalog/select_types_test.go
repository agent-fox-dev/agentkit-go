package catalog_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-26-24: Selection and its five fields are accessible from an external
// package.
func TestSelectionFieldsAreExported_TS26_24(t *testing.T) {
	s := catalog.Selection{
		Provider: "p",
		Tier:     catalog.TierSimple,
		Model:    core.Model{ID: "x"},
		Effort:   core.EffortLow,
		Listed:   true,
	}
	if s.Provider != "p" {
		t.Errorf("Provider = %q, want p", s.Provider)
	}
	if s.Tier != catalog.TierSimple {
		t.Errorf("Tier = %q, want %q", s.Tier, catalog.TierSimple)
	}
	if s.Model.ID != "x" {
		t.Errorf("Model.ID = %q, want x", s.Model.ID)
	}
	if s.Effort != core.EffortLow {
		t.Errorf("Effort = %q, want %q", s.Effort, core.EffortLow)
	}
	if !s.Listed {
		t.Error("Listed = false, want true")
	}
}
