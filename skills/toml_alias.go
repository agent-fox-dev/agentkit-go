package skills

import "github.com/agentfox/agentkit-go/internal/toml"

// The manifest TOML reader moved to internal/toml when `plugins` needed it
// too (REQ-PLUGIN-05). It is aliased back here rather than re-exported by
// value so that a *Table produced by either package is the same type — a
// parallel definition would compile and then fail at the one call site that
// crosses between them.
//
// It stays INTERNAL. It reads only the value forms a manifest uses and skips
// the rest with a diagnostic (REQ-SKILL-10 decodes locally authored manifests
// leniently, which is the only contract it is built to meet).

type (
	Table       = toml.Table
	Value       = toml.Value
	ValueKind   = toml.ValueKind
	SyntaxError = toml.SyntaxError
)

const (
	KindString      = toml.KindString
	KindBool        = toml.KindBool
	KindInt         = toml.KindInt
	KindStringArray = toml.KindStringArray
)

// ParseTOML parses a manifest; see internal/toml.
func ParseTOML(src []byte) (*Table, []Diagnostic, error) { return toml.ParseTOML(src) }
