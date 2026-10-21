package ollama

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
)

type blockKind int

const (
	textBlock blockKind = iota
	thinkingBlock
	toolBlock
)

// block is one content block of the turn, in order of arrival.
type block struct {
	kind  blockKind
	index int
	// text is the text of a text block or the thinking of a thinking block.
	text strings.Builder
	// tool is the finished tool call of a tool block.
	tool core.ToolUseBlock
}

// content is the block as the content block it ends as.
func (b *block) content() core.ContentBlock {
	switch b.kind {
	case thinkingBlock:
		return core.ThinkingBlock{Thinking: b.text.String()}
	case toolBlock:
		return b.tool
	}
	return core.TextBlock{Text: b.text.String()}
}

// callIDs makes the ids of tool calls the server sent without one. The ids are
// unique across the responses of one Client, and a per-Client random prefix keeps
// them apart from those of another Client that wrote earlier turns of the same
// transcript.
type callIDs struct {
	prefix string
	n      atomic.Uint64
}

func newCallIDs() *callIDs {
	var raw [4]byte
	// crypto/rand.Read does not fail on a supported platform.
	_, _ = rand.Read(raw[:])
	return &callIDs{prefix: hex.EncodeToString(raw[:])}
}

// next returns an id nothing else has used.
func (g *callIDs) next() string {
	return "ollama_" + g.prefix + "_" + strconv.FormatUint(g.n.Add(1), 10)
}

// decodeState turns the chunks of one turn into events and an assistant
// message. It is not safe for concurrent use: one Stream call owns it.
type decodeState struct {
	s *core.EventStream
	// partial is the message so far: the model, the effort and the time, and
	// every completed block.
	partial core.AssistantMessage
	started bool // the MessageStartEvent is out

	blocks []*block
	// open is the text or thinking block the next delta of its kind extends.
	open *block

	// ids makes an id for a tool call without one; used holds the ids of this
	// turn's calls, so an id the server repeats is replaced too.
	ids  *callIDs
	used map[string]bool

	sawDone bool
	last    api.ChatResponse // the chunk with done: true
}

func newDecodeState(s *core.EventStream, c *Client, req core.Request) *decodeState {
	return &decodeState{
		s: s,
		partial: core.AssistantMessage{
			Model:     c.model.ID,
			Effort:    req.Effort,
			Timestamp: c.now(),
		},
		ids:  c.ids,
		used: map[string]bool{},
	}
}

// start emits the MessageStartEvent, once, with the first chunk.
func (d *decodeState) start() {
	if d.started {
		return
	}
	d.started = true
	d.s.Push(core.MessageStartEvent{Message: d.partial})
}

// add appends a block of the kind.
func (d *decodeState) add(kind blockKind) *block {
	b := &block{kind: kind, index: len(d.blocks)}
	d.blocks = append(d.blocks, b)
	return b
}

// chunk decodes one chunk of the stream: its thinking, its content and its tool
// calls, in that order, and then whether it is the last.
func (d *decodeState) chunk(r api.ChatResponse) error {
	d.start()
	if r.Message.Thinking != "" {
		d.delta(thinkingBlock, r.Message.Thinking)
	}
	if r.Message.Content != "" {
		d.delta(textBlock, r.Message.Content)
	}
	for _, tc := range r.Message.ToolCalls {
		d.toolCall(tc)
	}
	if r.Done {
		d.sawDone, d.last = true, r
	}
	return nil
}

// delta adds text to the open block of the kind, closing the open block of the
// other kind and opening a new one first when there is none.
func (d *decodeState) delta(kind blockKind, s string) {
	b := d.open
	if b == nil || b.kind != kind {
		d.closeOpen()
		b = d.add(kind)
		d.open = b
		if kind == thinkingBlock {
			d.s.Push(core.ThinkingStartEvent{BlockIndex: b.index})
		} else {
			d.s.Push(core.TextStartEvent{BlockIndex: b.index})
		}
	}
	b.text.WriteString(s)
	if kind == thinkingBlock {
		d.s.Push(core.ThinkingDeltaEvent{BlockIndex: b.index, Delta: s})
	} else {
		d.s.Push(core.TextDeltaEvent{BlockIndex: b.index, Delta: s})
	}
}

// closeOpen completes the open text or thinking block: it is appended to the
// partial message, and a snapshot of the message follows.
func (d *decodeState) closeOpen() {
	if d.open == nil {
		return
	}
	d.appendBlock(d.open)
	d.open = nil
}

// appendBlock adds a completed block to the partial message and pushes the
// message as it now is.
func (d *decodeState) appendBlock(b *block) {
	d.partial.Content = append(d.partial.Content, b.content())
	d.s.Push(core.MessageUpdateEvent{Message: d.partial})
}

// toolCall adds a tool call, which Ollama sends whole: the start event, one
// delta with all the arguments, and the block. The arguments are the JSON the
// SDK's ordered map writes, with the keys in the order the server sent them.
func (d *decodeState) toolCall(tc api.ToolCall) {
	d.closeOpen()
	b := d.add(toolBlock)
	id := tc.ID
	if id == "" || d.used[id] {
		id = d.ids.next()
	}
	d.used[id] = true
	// The arguments came from JSON, so neither step fails.
	args, _ := json.Marshal(tc.Function.Arguments)
	b.tool, _ = core.NewToolUse(id, tc.Function.Name, args)
	d.s.Push(core.ToolCallStartEvent{BlockIndex: b.index, ToolUseID: b.tool.ID, Name: b.tool.Name})
	d.s.Push(core.ToolInputDeltaEvent{BlockIndex: b.index, ToolUseID: b.tool.ID, Delta: string(b.tool.Input)})
	d.appendBlock(b)
}

// stopReason maps done_reason to a canonical stop reason: length wins, even
// with tool calls; otherwise a message with tool calls stops for them.
func stopReason(doneReason string, hasToolCalls bool) core.StopReason {
	switch {
	case doneReason == "length":
		return core.StopReasonLength
	case hasToolCalls:
		return core.StopReasonToolUse
	}
	return core.StopReasonStop
}

// finish ends the turn: the open block closes, the block-end events go out in
// block order, and then the message.
func (d *decodeState) finish() {
	d.closeOpen()
	hasToolCalls := false
	for _, b := range d.blocks {
		switch b.kind {
		case textBlock:
			d.s.Push(core.TextEndEvent{BlockIndex: b.index, Text: b.text.String()})
		case thinkingBlock:
			d.s.Push(core.ThinkingEndEvent{BlockIndex: b.index, Thinking: b.text.String()})
		case toolBlock:
			hasToolCalls = true
			d.s.Push(core.ToolCallEndEvent{BlockIndex: b.index, Block: b.tool})
		}
	}
	final := d.partial
	final.StopReason = stopReason(d.last.DoneReason, hasToolCalls)
	final.RawStopReason = d.last.DoneReason
	final.ResponseModel = d.last.Model
	final.Usage = nettedUsage(d.last)
	d.s.Push(core.MessageEndEvent{Message: final})
	d.s.End(core.StreamResult{Message: &final})
}

// fail ends the turn with an error: the message keeps the text and thinking
// streamed so far, and never a tool call, because a call is not known to be
// complete before the done chunk. A turn the caller cancelled ends as aborted
// instead, with no ErrorEvent.
func (d *decodeState) fail(text string, err error) {
	d.closeOpen()
	final := d.partial
	final.Content = nil
	for _, blk := range d.partial.Content {
		if _, isTool := blk.(core.ToolUseBlock); !isTool {
			final.Content = append(final.Content, blk)
		}
	}
	final.Usage = core.Usage{Requests: 1}
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
