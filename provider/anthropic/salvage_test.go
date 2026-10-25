package anthropic_test

import (
	"encoding/json"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// Valid input comes back byte for byte, key order and spacing included: a
// repair that normalized it would shift the prompt-cache prefix every turn.
func TestSalvageJSONReturnsValidInputUnchanged(t *testing.T) {
	for _, in := range []string{`{"zebra":1,"apple":2}`, `{ "spaced" : [ 1 , 2 ] }`, `{"nested":{"b":1,"a":2}}`, `{}`, `12`} {
		if out, repaired := anthropic.SalvageJSON([]byte(in)); repaired || string(out) != in {
			t.Errorf("SalvageJSON(%s) = %s, %v; want it unchanged", in, out, repaired)
		}
	}
}

// A truncated member, or one whose container is still open at the cut, is
// dropped, never closed, so the truncation surfaces as a missing argument
// rather than as a valid call naming a file nobody named.
func TestSalvageJSONDropsTheTruncatedMember(t *testing.T) {
	for in, want := range map[string]string{
		`{"path":"/etc/pas`:                   `{}`,
		`{"a":1,"path":"/etc/pas`:             `{"a":1}`,
		`{"a":1,"path"`:                       `{"a":1}`,
		`{"a":1,"path":`:                      `{"a":1}`,
		`{"a":1,`:                             `{"a":1}`,
		`{"a":1`:                              `{}`,
		`{"a":tru`:                            `{}`,
		`{"a":"x\"y`:                          `{}`,
		`{"a":{"b":"x","c":"y`:                `{}`,
		`{"a":[1,2`:                           `{}`,
		`{"a":"x","b":{"c":1`:                 `{"a":"x"}`,
		`{"path":"/x","edits":[{"old":"a"},{`: `{"path":"/x"}`,
		`{"a":{"b":1},"c":"y`:                 `{"a":{"b":1}}`,
		`["a","b`:                             `["a"]`,
		``:                                    `{}`,
		" \n\t":                               `{}`,
		`{`:                                   `{}`,
		`}`:                                   `{}`,
		`tru`:                                 `{}`,
		"{\"a\":1}\n}":                        `{}`,
	} {
		out, repaired := anthropic.SalvageJSON([]byte(in))
		if !repaired || string(out) != want {
			t.Errorf("SalvageJSON(%q) = %s, %v; want %s", in, out, repaired, want)
		}
	}
}

// Whatever arrives, the result parses.
func FuzzSalvageAlwaysProducesValidJSON(f *testing.F) {
	for _, s := range []string{`{"a":1}`, `{"a":"\u00`, `{"a":"x\\`, `[[[[`, `{"a":tru`, `{"a":1e`, `{"":""`,
		`{"a":[{"b":`, "\x00", `{"a":"\"`, `{"a" 1}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if out, _ := anthropic.SalvageJSON(b); !json.Valid(out) {
			t.Fatalf("SalvageJSON(%q) = %q, which is not JSON", b, out)
		}
	})
}
