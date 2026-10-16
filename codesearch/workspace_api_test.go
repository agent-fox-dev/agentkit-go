//go:build !windows

package codesearch

import (
	"reflect"
	"testing"
)

// TS-20-48: codesearch.Options declares no Ignore field: ignore rules are the
// workspace's, given once to workspace.Open.
func TestOptionsDeclareNoIgnoreField_TS20_48(t *testing.T) {
	typ := reflect.TypeFor[Options]()
	if _, ok := typ.FieldByName("Ignore"); ok {
		t.Error("codesearch.Options still declares an Ignore field")
	}
	for _, name := range []string{"MaxFiles", "MaxBytes", "MaxBuildTime", "TempDir"} {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("codesearch.Options lost its %s field", name)
		}
	}
}

// TS-20-53: codesearch.New with a nil workspace returns no index and the error
// text it returned before the workspace package existed.
func TestNewWithANilWorkspaceKeepsItsError_TS20_53(t *testing.T) {
	idx, err := New(nil, Options{})
	if idx != nil {
		t.Errorf("New(nil, ...) returned %T(%v), want a nil interface", idx, idx)
	}
	// The text recorded from the code before this change.
	const want = "codesearch: workspace must not be nil"
	if err == nil || err.Error() != want {
		t.Errorf("New(nil, ...) err = %v, want %q", err, want)
	}
}
