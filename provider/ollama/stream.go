package ollama

import (
	"context"

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
	chatReq, err := c.chatRequest(req)
	if err != nil {
		d.fail(err.Error(), err)
		return
	}
	a := &attempt{}
	err = c.api.Chat(context.WithValue(ctx, attemptKey{}, a), chatReq, d.chunk)
	switch {
	case err != nil:
		d.fail("ollama: "+err.Error(), err)
	case !d.sawDone:
		d.fail(ErrStreamTruncated.Error(), ErrStreamTruncated)
	default:
		d.finish()
	}
}
