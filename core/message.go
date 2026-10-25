// Package core holds AgentKit's canonical vocabulary and every interface seam.
// Machinery lives elsewhere; core holds declarations, the small pure functions
// that are part of the contract, and the EventStream.
//
// core imports only schema. Nothing in AgentKit below the root
// package may import the root package, which is what keeps anything that
// needs an *Agent out of core.
package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type Role string

const (
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "tool_result"
)

// Message is a sealed union: UserMessage | AssistantMessage | ToolResultMessage.
//
// An interface, not a Role-discriminated fat struct. ToolResultMessage is a
// first-class role whose ToolUseID, IsError, ToolName and Usage exist on no
// other role. A fat struct puts those fields on every message and makes
// "flatten results into a shared user message" a one-line accident instead of
// a type error. Sealed by isMessage() so provider adapters' role switches stay
// total.
type Message interface {
	Role() Role
	Clone() Message
	isMessage()
}

type Messages []Message

func (ms Messages) Clone() Messages {
	if ms == nil {
		return nil
	}
	out := make(Messages, len(ms))
	for i, m := range ms {
		out[i] = m.Clone()
	}
	return out
}

type UserMessage struct {
	Content   Content
	Timestamp time.Time
}

type AssistantMessage struct {
	Content Content

	// StopReason is the canonical enum; RawStopReason is the provider's own
	// finish string, verbatim. Neither may be read by the continuation check
	// — use ExtractToolUse.
	StopReason    StopReason
	RawStopReason string
	// StopDetail is the provider's own account of why it stopped, when it
	// gives one beyond the reason — a refusal's category and explanation
	// ("cyber: …"). Like RawStopReason it never drives control flow.
	StopDetail   string
	ErrorMessage string
	Usage        Usage
	Timestamp    time.Time

	// Model is the REQUESTED model, never ResponseModel: transcript repair
	// replays a turn's thinking signatures only to the model that issued
	// them, and a fallback-served turn judged against ResponseModel would
	// have its own valid signatures stripped.
	Model         string
	ResponseModel string
	ResponseID    string
	// Effort is the thinking effort the turn was requested with.
	Effort Effort
}

type ToolResultMessage struct {
	ToolUseID string
	ToolName  string
	Content   Content
	IsError   bool
	Timestamp time.Time
	// AddedToolNames marks tools that became available at this point in the
	// transcript. Opaque to the loop.
	AddedToolNames []string
	Usage          *Usage // optional; a pointer, never omitempty
	// Metadata is the producing handler's structured metadata (exit code,
	// outcome, truncation, spill path, totals, duration, line ending).
	// Persisted by the session codec, never sent to a provider. Nil for
	// handler-less results (unknown tool, invalid arguments, blocked,
	// aborted, max_tokens synthesis, repair synthesis).
	Metadata *ToolMetadata
}

func (UserMessage) Role() Role       { return RoleUser }
func (AssistantMessage) Role() Role  { return RoleAssistant }
func (ToolResultMessage) Role() Role { return RoleToolResult }

func (UserMessage) isMessage()       {}
func (AssistantMessage) isMessage()  {}
func (ToolResultMessage) isMessage() {}

func (m UserMessage) Clone() Message {
	m.Content = m.Content.Clone()
	return m
}

func (m AssistantMessage) Clone() Message {
	m.Content = m.Content.Clone()
	return m
}

func (m ToolResultMessage) Clone() Message {
	m.Content = m.Content.Clone()
	m.AddedToolNames = append([]string(nil), m.AddedToolNames...)
	if m.Usage != nil {
		u := *m.Usage
		m.Usage = &u
	}
	m.Metadata = m.Metadata.Clone()
	return m
}

// ---------------------------------------------------------------- content

type BlockType string

const (
	BlockText     BlockType = "text"
	BlockThinking BlockType = "thinking"
	BlockToolUse  BlockType = "tool_use"
)

// ContentBlock is a sealed union: TextBlock | ThinkingBlock | ToolUseBlock.
// Sealed because every provider adapter switches exhaustively over it and a
// third-party variant would make that switch silently incomplete.
type ContentBlock interface {
	BlockType() BlockType
	CloneBlock() ContentBlock
	isContentBlock()
}

type Content []ContentBlock

func (c Content) Clone() Content {
	if c == nil {
		return nil
	}
	out := make(Content, len(c))
	for i, b := range c {
		out[i] = b.CloneBlock()
	}
	return out
}

// Text concatenates every TextBlock, for RunResult.FinalText.
func (c Content) Text() string {
	var s string
	for _, b := range c {
		if t, ok := b.(TextBlock); ok {
			s += t.Text
		}
	}
	return s
}

// TextBlock holds a Go string, not []byte, deliberately: strings are
// immutable, so the deep copy EventStream takes at every Push is a header
// copy rather than a byte copy. Cloning a partial assistant message is
// therefore O(blocks), not O(streamed characters).
type TextBlock struct{ Text string }

type ThinkingBlock struct {
	Thinking string
	// Signature is provider-issued and opaque; it may be empty on an aborted
	// stream, and transcript repair downgrades an unsigned block to text.
	// Never inspected.
	Signature string
	Redacted  bool
}

type ToolUseBlock struct {
	ID   string
	Name string
	// Input is the provider's own argument bytes, verbatim and AUTHORITATIVE
	// for every replay, fingerprint and serialization path.
	// Invariant: always syntactically valid JSON object bytes; NewToolUse
	// normalizes nil/empty to {}.
	Input json.RawMessage
	// ThoughtSignature is opaque and provider-issued; transcript repair strips
	// it on cross-model replay. Never inspected.
	ThoughtSignature string
}

// NewToolUse checks raw is a JSON object and keeps its bytes. A nil, empty
// or whitespace-only raw yields {}.
func NewToolUse(id, name string, raw json.RawMessage) (ToolUseBlock, error) {
	b := ToolUseBlock{ID: id, Name: name}
	trimmed := trimSpace(raw)
	if len(trimmed) == 0 {
		b.Input = json.RawMessage("{}")
		return b, nil
	}
	if _, err := decodeObject(trimmed); err != nil {
		return b, err
	}
	b.Input = append(json.RawMessage(nil), trimmed...)
	return b, nil
}

// errNotObject is a tool input that is valid JSON but not an object.
var errNotObject = errors.New("core: tool input is not a JSON object")

// decodeObject decodes a JSON object with numbers kept as json.Number, so a
// numeric literal survives verbatim.
func decodeObject(raw []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	// More() reports false for a stray ] or }, so the decoder is asked for
	// one more token: only the end of the input will do.
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("core: trailing data after the tool input")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errNotObject
	}
	return m, nil
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && isSpace(b[i]) {
		i++
	}
	for j > i && isSpace(b[j-1]) {
		j--
	}
	return b[i:j]
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// InputMap decodes lazily for handler and interceptor convenience. The result
// is NEVER re-encoded back into a request.
//
// Numbers come back as json.Number, not float64, because numeric literals
// must survive verbatim. Handlers doing args["limit"].(float64) will fail
// their type assertion; assert json.Number and call its Int64 or Float64.
func (b ToolUseBlock) InputMap() map[string]any {
	m, err := decodeObject(b.Input)
	if err != nil {
		return nil
	}
	return m
}

func (TextBlock) BlockType() BlockType     { return BlockText }
func (ThinkingBlock) BlockType() BlockType { return BlockThinking }
func (ToolUseBlock) BlockType() BlockType  { return BlockToolUse }

func (TextBlock) isContentBlock()     {}
func (ThinkingBlock) isContentBlock() {}
func (ToolUseBlock) isContentBlock()  {}

func (b TextBlock) CloneBlock() ContentBlock     { return b }
func (b ThinkingBlock) CloneBlock() ContentBlock { return b }

func (b ToolUseBlock) CloneBlock() ContentBlock {
	b.Input = append(json.RawMessage(nil), b.Input...)
	return b
}
