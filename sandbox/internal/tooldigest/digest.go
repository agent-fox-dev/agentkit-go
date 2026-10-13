// Package tooldigest computes a tool's definition digest. The host and the
// agent both use Digest, so each can tell when the other's tool definitions
// differ.
package tooldigest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// Digest is the SHA-256 over t's name, description, input and output schema
// JSON, prompt guidelines, Builtin flag and execution mode. Every field is
// length-prefixed, so the encoding is injective: two tools have equal
// digests exactly when those fields are equal.
func Digest(t core.Tool) []byte {
	h := sha256.New()
	field := func(b []byte) {
		h.Write(binary.BigEndian.AppendUint64(nil, uint64(len(b))))
		h.Write(b)
	}
	field([]byte(t.Name))
	field([]byte(t.Description))
	field(schemaJSON(t.InputSchema))
	field(schemaJSON(t.OutputSchema))
	field(binary.BigEndian.AppendUint64(nil, uint64(len(t.PromptGuidelines))))
	for _, g := range t.PromptGuidelines {
		field([]byte(g))
	}
	builtin := byte(0)
	if t.Builtin {
		builtin = 1
	}
	field([]byte{builtin, byte(t.ExecutionMode)})
	return h.Sum(nil)
}

// schemaJSON is s marshalled, or a marker that no valid JSON starts with
// when it cannot be.
func schemaJSON(s *schema.Schema) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		return []byte("!" + err.Error())
	}
	return b
}
