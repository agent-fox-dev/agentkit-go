package schema

import (
	"bytes"
	"encoding/json"
	"testing"
)

// decode reads s the way core.PrepareArguments does: numbers as json.Number.
func decode(t *testing.T, s string) any {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return v
}

func obj(t *testing.T, s string) map[string]any {
	t.Helper()
	return decode(t, s).(map[string]any)
}

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func intp(n int) *int           { return &n }
func floatp(f float64) *float64 { return &f }
