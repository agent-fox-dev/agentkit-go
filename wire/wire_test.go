package wire_test

import (
	"errors"
	"io"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/wire"
)

func TestLimitsWithDefaultsFillsOnlyTheZeroFields(t *testing.T) {
	d := wire.Defaults()
	if d != (wire.Limits{MaxMessageBytes: 16 << 20, MaxContainerLen: 1_000_000, MaxDepth: 64, MaxNodes: 2_000_000}) {
		t.Fatalf("Defaults() = %+v", d)
	}
	if got := (wire.Limits{}).WithDefaults(); got != d {
		t.Fatalf("zero Limits.WithDefaults() = %+v, want %+v", got, d)
	}
	got := wire.Limits{MaxDepth: 3, MaxNodes: -1}.WithDefaults()
	if got.MaxDepth != 3 || got.MaxNodes != d.MaxNodes || got.MaxMessageBytes != d.MaxMessageBytes {
		t.Fatalf("WithDefaults() = %+v, want MaxDepth kept and the rest defaulted", got)
	}
}

func TestErrorNamesItsRuleAndPathAndUnwrapsTheCause(t *testing.T) {
	e := &wire.Error{Rule: wire.RuleDepth, Msg: "too deep", Err: io.ErrUnexpectedEOF}
	if got := e.Error(); got != "wire: depth at $: too deep" {
		t.Errorf("Error() with no path = %q", got)
	}
	e.Path = "$.a[0]"
	if got := e.Error(); got != "wire: depth at $.a[0]: too deep" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(e, wire.ErrRejected) || !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Error("an Error must match ErrRejected and its wrapped cause")
	}
}
