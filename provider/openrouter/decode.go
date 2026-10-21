package openrouter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OpenRouterTeam/go-sdk/models/components"

	"github.com/agent-fox-dev/agentkit-go/core"
)

type blockKind int

const (
	textBlock blockKind = iota
	thinkingBlock
	toolBlock
)

// block accumulates one content block of the turn.
type block struct {
	kind  blockKind
	index int // the block index, in order of first appearance

	// text is the text of a text block, or the visible text of a thinking one.
	text strings.Builder
	// fragments are the reasoning_details elements of a thinking block, each as
	// JSON, in arrival order.
	fragments []json.RawMessage

	// A tool call: its index in the chunks, id and name, the concatenated
	// arguments, whether its start event is out, and the argument fragments
	// that arrived before it could be.
	toolIndex int64
	id, name  string
	args      strings.Builder
	started   bool
	unsent    []string
}

// signature is the thinking block's fragments as a JSON array, or "" when none
// arrived.
func (b *block) signature() string {
	if len(b.fragments) == 0 {
		return ""
	}
	parts := make([]string, len(b.fragments))
	for i, f := range b.fragments {
		parts[i] = string(f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// decodeState turns the chunks of one turn into events and an assistant
// message. It is not safe for concurrent use: one run owns it.
type decodeState struct {
	s      *core.EventStream
	warnf  func(format string, args ...any)
	redact func(string) string // takes the API key out of a text

	// model is the model the turn asked for; the usage is priced against it.
	model core.Model
	// partial is the message as it starts: the model, the effort and the time.
	partial core.AssistantMessage
	started bool // the MessageStartEvent is out

	blocks []*block
	tools  map[int64]*block // the tool-call block of each chunk index
	last   *block           // the block the next delta of the same kind extends
	ids    map[string]bool  // tool-call ids used in the turn

	usage         *components.ChatUsage // the last usage the response reported
	finishRaw     string
	sawFinish     bool
	responseID    string
	responseModel string
}

func newDecodeState(s *core.EventStream, c *client, req core.Request) *decodeState {
	return &decodeState{
		s:      s,
		warnf:  c.warnf,
		redact: c.redact,
		model:  c.model,
		partial: core.AssistantMessage{
			Model:     c.model.ID,
			Effort:    req.Effort,
			Timestamp: c.now(),
		},
		tools: map[int64]*block{},
		ids:   map[string]bool{},
	}
}

// reset forgets what an attempt that failed before any content had read, so the
// next attempt starts clean. Nothing was emitted, so the consumer saw none of it.
func (d *decodeState) reset() {
	d.blocks, d.last = nil, nil
	d.tools = map[int64]*block{}
	d.ids = map[string]bool{}
	d.usage = nil
	d.finishRaw, d.sawFinish = "", false
	d.responseID, d.responseModel = "", ""
}

// start emits the MessageStartEvent, once, before the first content event.
func (d *decodeState) start() {
	if d.started {
		return
	}
	d.started = true
	d.s.Push(core.MessageStartEvent{Message: d.partial})
}

// open adds a block, which the next delta of its kind extends.
func (d *decodeState) open(kind blockKind) *block {
	b := &block{kind: kind, index: len(d.blocks)}
	d.blocks = append(d.blocks, b)
	d.last = b
	return b
}

// chunk decodes one chunk of the stream. It returns the error the chunk reports
// when it is an in-stream error: an error object, or a choice that finished
// with "error".
func (d *decodeState) chunk(c components.ChatStreamChunk) *StatusError {
	if c.ID != "" {
		d.responseID = c.ID
	}
	if c.Model != "" {
		d.responseModel = c.Model
	}
	if c.Usage != nil {
		d.usage = c.Usage
	}
	finishedWithError := false
	for _, choice := range c.Choices {
		if choice.Index != 0 {
			continue
		}
		d.thinking(choice.Delta)
		if s, _ := choice.Delta.Content.GetOrZero(); s != "" {
			d.text(s)
		}
		for _, tc := range choice.Delta.ToolCalls {
			d.toolFragment(tc)
		}
		if choice.FinishReason != nil {
			if *choice.FinishReason == components.ChatFinishReasonEnumError {
				finishedWithError = true
				continue
			}
			d.sawFinish = true
			d.finishRaw = string(*choice.FinishReason)
		}
	}
	switch {
	case c.Error != nil:
		return &StatusError{Code: c.Error.Code,
			Text: d.redact(fmt.Sprintf("openrouter: error %d in stream: %s", c.Error.Code, c.Error.Message))}
	case finishedWithError:
		return &StatusError{Text: `openrouter: error in stream: finish_reason "error"`}
	}
	return nil
}

// text adds a content delta to the open text block, opening one when the last
// block is of another kind.
func (d *decodeState) text(s string) {
	d.start()
	b := d.last
	if b == nil || b.kind != textBlock {
		b = d.open(textBlock)
		d.s.Push(core.TextStartEvent{BlockIndex: b.index})
	}
	b.text.WriteString(s)
	d.s.Push(core.TextDeltaEvent{BlockIndex: b.index, Delta: s})
}

// thinking adds a delta's reasoning to the open thinking block. The visible
// text is delta.reasoning when the chunk carries it, otherwise the text of its
// reasoning.text fragments and the summary of its reasoning.summary ones; an
// encrypted fragment adds none. Every fragment is kept, with all its fields.
func (d *decodeState) thinking(delta components.ChatStreamDelta) {
	visible, _ := delta.Reasoning.GetOrZero()
	var fragments []json.RawMessage
	var fromFragments strings.Builder
	for _, rd := range delta.ReasoningDetails {
		raw, err := json.Marshal(rd)
		if err != nil {
			d.warnf("openrouter: dropped a reasoning fragment that could not be encoded: %v", err)
			continue
		}
		fragments = append(fragments, raw)
		fromFragments.WriteString(fragmentText(rd))
	}
	if visible == "" {
		visible = fromFragments.String()
	}
	if visible == "" && len(fragments) == 0 {
		return
	}
	d.start()
	b := d.last
	if b == nil || b.kind != thinkingBlock {
		b = d.open(thinkingBlock)
		d.s.Push(core.ThinkingStartEvent{BlockIndex: b.index})
	}
	b.fragments = append(b.fragments, fragments...)
	if visible != "" {
		b.text.WriteString(visible)
		d.s.Push(core.ThinkingDeltaEvent{BlockIndex: b.index, Delta: visible})
	}
}

// fragmentText is the visible text of a reasoning fragment.
func fragmentText(rd components.ReasoningDetailUnion) string {
	switch {
	case rd.ReasoningDetailText != nil:
		s, _ := rd.ReasoningDetailText.Text.GetOrZero()
		return s
	case rd.ReasoningDetailSummary != nil:
		return rd.ReasoningDetailSummary.Summary
	}
	return ""
}

// toolFragment adds a tool-call fragment to the call of its index. The call's
// start event goes out at the first fragment that carries its name (and its id,
// or none: a call that arrives without an id is given one).
func (d *decodeState) toolFragment(tc components.ChatStreamToolCall) {
	b := d.tools[tc.Index]
	if b == nil {
		b = d.open(toolBlock)
		b.toolIndex = tc.Index
		d.tools[tc.Index] = b
	}
	d.last = b
	if tc.ID != nil && *tc.ID != "" && b.id == "" {
		b.id = *tc.ID
	}
	var args string
	if fn := tc.Function; fn != nil {
		if fn.Name != nil && *fn.Name != "" && b.name == "" {
			b.name = *fn.Name
		}
		if fn.Arguments != nil {
			args = *fn.Arguments
		}
	}
	b.args.WriteString(args)

	if !b.started && b.name != "" {
		d.start()
		if b.id == "" {
			b.id = d.newID()
		}
		d.ids[b.id] = true
		b.started = true
		d.s.Push(core.ToolCallStartEvent{BlockIndex: b.index, ToolUseID: b.id, Name: b.name})
		for _, u := range b.unsent {
			d.s.Push(core.ToolInputDeltaEvent{BlockIndex: b.index, ToolUseID: b.id, Delta: u})
		}
		b.unsent = nil
	}
	switch {
	case args == "":
	case b.started:
		d.s.Push(core.ToolInputDeltaEvent{BlockIndex: b.index, ToolUseID: b.id, Delta: args})
	default:
		b.unsent = append(b.unsent, args)
	}
}

// newID returns a tool-call id, call_ and random hex, unused in this turn.
func (d *decodeState) newID() string {
	for {
		var raw [8]byte
		// crypto/rand.Read does not fail on a supported platform.
		_, _ = rand.Read(raw[:])
		if id := "call_" + hex.EncodeToString(raw[:]); !d.ids[id] {
			return id
		}
	}
}

// finals returns each block as the content block it ends as. A tool call whose
// arguments are not a JSON object ends with the input {}, and the warning names
// the call. A tool call that never got a name is dropped.
func (d *decodeState) finals() (blocks []*block, content core.Content) {
	for _, b := range d.blocks {
		switch b.kind {
		case textBlock:
			content = append(content, core.TextBlock{Text: b.text.String()})
		case thinkingBlock:
			content = append(content, core.ThinkingBlock{Thinking: b.text.String(), Signature: b.signature()})
		case toolBlock:
			if !b.started {
				d.warnf("openrouter: dropped a tool call that never named a function (index %d)", b.toolIndex)
				continue
			}
			content = append(content, d.toolUse(b))
		}
		blocks = append(blocks, b)
	}
	return blocks, content
}

// toolUse is the tool-use block of a finished call.
func (d *decodeState) toolUse(b *block) core.ToolUseBlock {
	tu, err := core.NewToolUse(b.id, b.name, json.RawMessage(b.args.String()))
	if err != nil {
		d.warnf("openrouter: tool call %s (%s) has arguments that are not a JSON object; using {}", b.id, b.name)
		tu, _ = core.NewToolUse(b.id, b.name, nil)
	}
	return tu
}

// mapStop maps a finish_reason to a canonical stop reason.
func mapStop(raw string) core.StopReason {
	switch raw {
	case "length":
		return core.StopReasonLength
	case "tool_calls":
		return core.StopReasonToolUse
	case "content_filter":
		return core.StopReasonRefusal
	}
	return core.StopReasonStop
}

// finish ends the turn: the block-end events in block order, then the message.
func (d *decodeState) finish() {
	d.start()
	blocks, content := d.finals()
	for i, b := range blocks {
		switch v := content[i].(type) {
		case core.TextBlock:
			d.s.Push(core.TextEndEvent{BlockIndex: b.index, Text: v.Text})
		case core.ThinkingBlock:
			d.s.Push(core.ThinkingEndEvent{BlockIndex: b.index, Thinking: v.Thinking, Signature: v.Signature, Redacted: v.Redacted})
		case core.ToolUseBlock:
			d.s.Push(core.ToolCallEndEvent{BlockIndex: b.index, Block: v})
		}
	}
	final := d.partial
	final.Content = content
	final.StopReason = mapStop(d.finishRaw)
	final.RawStopReason = d.finishRaw
	final.ResponseID = d.responseID
	final.ResponseModel = d.responseModel
	final.Usage = d.finalUsage()
	d.s.Push(core.MessageEndEvent{Message: final})
	d.s.End(core.StreamResult{Message: &final})
}

// fail ends the turn with an error: the message keeps the text and thinking
// streamed so far, and never a tool call, because a Chat Completions tool call
// is not known to be complete before its finish_reason. A turn the caller
// cancelled ends as aborted instead.
func (d *decodeState) fail(text string, err error) {
	text = d.redact(text)
	if msg := err.Error(); d.redact(msg) != msg {
		err = &redacted{msg: d.redact(msg), err: err}
	}
	final := d.partial
	for _, b := range d.blocks {
		switch b.kind {
		case textBlock:
			final.Content = append(final.Content, core.TextBlock{Text: b.text.String()})
		case thinkingBlock:
			final.Content = append(final.Content, core.ThinkingBlock{Thinking: b.text.String(), Signature: b.signature()})
		}
	}
	final.ResponseID = d.responseID
	final.ResponseModel = d.responseModel
	final.Usage = d.finalUsage()
	if text == abortText {
		final.StopReason = core.StopReasonAborted
		final.ErrorMessage = text
		d.s.Push(core.MessageEndEvent{Message: final})
		d.s.End(core.StreamResult{Message: &final, Err: core.ErrAborted})
		return
	}
	final.StopReason = core.StopReasonError
	final.ErrorMessage = text
	d.s.Push(core.ErrorEvent{Message: text, Err: err, Terminal: true})
	d.s.Push(core.MessageEndEvent{Message: final})
	d.s.End(core.StreamResult{Message: &final, Err: err})
}
