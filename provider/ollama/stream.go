package ollama

import (
	"context"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// abortText is the ErrorMessage of a turn the caller cancelled.
const abortText = "Request was aborted"

// Stream sends the request and returns the turn's events. A context that is
// already done is refused with its error and no channel; every failure after
// that is encoded in the stream.
func (c *Client) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := core.NewEventStream(core.StreamOptions{})
	go c.run(ctx, s, req)
	return core.StreamChannel(s), nil
}

// run sends the request once and decodes its stream into s.
func (c *Client) run(ctx context.Context, s *core.EventStream, req core.Request) {
	d := newDecodeState(s, c, req)
	a := &attempt{}
	err := c.api.Chat(context.WithValue(ctx, attemptKey{}, a), c.chatRequest(req), d.chunk)
	switch {
	case err != nil:
		d.fail("ollama: "+err.Error(), err)
	case !d.sawDone:
		d.fail(ErrStreamTruncated.Error(), ErrStreamTruncated)
	default:
		d.finish()
	}
}

// chatRequest is the request body: the model as written, the messages,
// streaming, no truncation of the input, and the context length the model loads
// at.
func (c *Client) chatRequest(req core.Request) *api.ChatRequest {
	stream, truncate := true, false
	return &api.ChatRequest{
		Model:    c.model.ID,
		Messages: c.chatMessages(req),
		Stream:   &stream,
		Truncate: &truncate,
		Options:  map[string]any{"num_ctx": c.contextLength()},
	}
}

// contextLength is num_ctx: the model's window, else the catalog's default for an
// Ollama model with none. The window the agent prunes against must be the one
// the model loads at.
func (c *Client) contextLength() int {
	if c.model.ContextWindow > 0 {
		return c.model.ContextWindow
	}
	return catalog.OllamaDefaultContextWindow
}
