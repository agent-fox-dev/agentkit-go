package codemode

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Every zero field takes its default, a field that is set is kept, and the
// defaults are a fixed point.
func TestDefaultOptionsFillOnlyTheZeroFields(t *testing.T) {
	d := DefaultOptions()
	want := Options{Name: DefaultName, MaxTimeout: DefaultMaxTimeout, MaxSteps: DefaultMaxSteps, MaxCalls: DefaultMaxCalls,
		MaxConcurrentCalls: DefaultMaxConcurrentCalls, MaxOutputBytes: DefaultMaxOutputBytes,
		SpillDir: filepath.Join(os.TempDir(), "agentkit-codemode")}
	if !reflect.DeepEqual(d, want) || !reflect.DeepEqual(d.withDefaults(), d) {
		t.Fatalf("DefaultOptions() = %+v", d)
	}
	set := Options{Name: "n", MaxTimeout: time.Second, MaxSteps: 1, MaxCalls: 2, MaxConcurrentCalls: 3,
		MaxOutputBytes: 4, SpillDir: "/spill", DisableSpill: true}
	if got := set.withDefaults(); !reflect.DeepEqual(got, set) {
		t.Fatalf("withDefaults changed set fields: %+v", got)
	}
	if got := (Options{MaxTimeout: -1, MaxCalls: -1}).withDefaults(); got.MaxTimeout != DefaultMaxTimeout || got.MaxCalls != DefaultMaxCalls {
		t.Fatalf("negative limits = %+v, want the defaults", got)
	}
}
