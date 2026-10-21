package openrouter

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/operations"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// abortText is the ErrorMessage of a turn the caller cancelled.
const abortText = "Request was aborted"

// Stream sends the request and returns the turn's events. A context that is
// already done is refused with its error and no channel; every failure after
// that is encoded in the stream.
func (c *client) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := c.buildBody(req)
	if err != nil {
		return core.StreamChannel(core.ErrorStream(nil, fmt.Errorf("openrouter: building request: %w", err))), nil
	}
	s := core.NewEventStream(core.StreamOptions{})
	go c.run(ctx, s, req, body)
	return core.StreamChannel(s), nil
}

// run sends the request and decodes its stream into s. An attempt that fails
// with a 429 or a 5xx, as an HTTP status or as an error inside the stream before
// any content, is sent again up to the configured number of retries, after the
// wait the failure asks for; any other failure, and the last one, ends the turn.
func (c *client) run(ctx context.Context, s *core.EventStream, req core.Request, body []byte) {
	d := newDecodeState(s, c, req)
	for retries := 0; ; retries++ {
		f := c.once(ctx, d, body)
		if f == nil {
			return
		}
		if ctx.Err() != nil {
			d.fail(abortText, core.ErrAborted)
			return
		}
		if !f.retryable || retries >= c.maxRetries {
			d.fail(f.text, f.err)
			return
		}
		if err := c.sleep(ctx, retryDelay(retries, f.retryAfter, c.now(), rand.Float64)); err != nil {
			d.fail(abortText, core.ErrAborted)
			return
		}
		d.reset()
	}
}

// once sends the request one time and decodes its stream. It returns nil when
// the turn is over, finished or failed in a way already reported, and otherwise
// how the attempt failed.
func (c *client) once(ctx context.Context, d *decodeState, body []byte) *failure {
	a := &attempt{body: body}

	// The SDK serializes this placeholder; the transport replaces its body.
	model, stream := c.model.ID, true
	placeholder := components.ChatRequest{Model: &model, Stream: &stream, Messages: []components.ChatMessages{}}
	res, err := c.sdk.Chat.Send(context.WithValue(ctx, attemptKey{}, a), placeholder, nil,
		operations.WithAcceptHeaderOverride(operations.AcceptHeaderEnumTextEventStream))
	if err != nil {
		return c.sendFailure(ctx, a, err)
	}
	if res == nil || res.EventStream == nil {
		err := errors.New("openrouter: the response was not an event stream")
		return &failure{err: err, text: err.Error()}
	}
	events := res.EventStream
	defer events.Close()
	for events.Next() {
		if serr := d.chunk(events.Value().Data); serr != nil {
			return &failure{err: serr, text: serr.Text, retryable: retryableCode(serr.Code) && !d.started, retryAfter: a.retryAfter}
		}
	}
	if err := events.Err(); err != nil {
		return &failure{err: fmt.Errorf("openrouter: %w", err), text: failureText(ctx, err)}
	}
	if !d.sawFinish {
		return &failure{err: ErrStreamTruncated, text: ErrStreamTruncated.Error()}
	}
	d.finish()
	return nil
}

// sendFailure classifies an error from the SDK's send. A response with a status
// that is not 2xx is a StatusError, retryable when it is a 429 or a 5xx; a
// failure with no response is not.
func (c *client) sendFailure(ctx context.Context, a *attempt, err error) *failure {
	if a.status != 0 && (a.status < 200 || a.status >= 300) {
		se := c.httpError(a.status, a.errBody)
		return &failure{err: se, text: se.Text, retryable: retryableCode(a.status), retryAfter: a.retryAfter}
	}
	return &failure{err: fmt.Errorf("openrouter: %w", err), text: failureText(ctx, err)}
}

// failureText is the text of a turn that failed with err: the abort text when
// the caller cancelled, otherwise err's text under the package prefix.
func failureText(caller context.Context, err error) string {
	if caller.Err() != nil {
		return abortText
	}
	if errors.Is(err, ErrStreamTruncated) {
		return err.Error()
	}
	return "openrouter: " + err.Error()
}
