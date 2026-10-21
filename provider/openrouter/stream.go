package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/models/operations"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// abortText is the ErrorMessage of a turn the caller cancelled.
const abortText = "Request was aborted"

// errTruncated is a body that ended before any choice carried a finish_reason.
var errTruncated = errors.New("openrouter: stream ended before finish_reason")

// Stream sends the request and returns the turn's events. A context that is
// already done is refused with its error and no channel; every failure after
// that is encoded in the stream.
func (c *client) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := buildBody(c.model, req)
	if err != nil {
		return core.StreamChannel(core.ErrorStream(nil, fmt.Errorf("openrouter: building request: %w", err))), nil
	}
	s := core.NewEventStream(core.StreamOptions{})
	go c.run(ctx, s, req, body)
	return core.StreamChannel(s), nil
}

// run sends one request and decodes its stream into s.
func (c *client) run(ctx context.Context, s *core.EventStream, req core.Request, body []byte) {
	d := newDecodeState(s, c, req)

	// The SDK serializes this placeholder; the transport replaces its body.
	model, stream := c.model.ID, true
	placeholder := components.ChatRequest{Model: &model, Stream: &stream, Messages: []components.ChatMessages{}}
	res, err := c.sdk.Chat.Send(context.WithValue(ctx, attemptKey{}, &attempt{body: body}), placeholder, nil,
		operations.WithAcceptHeaderOverride(operations.AcceptHeaderEnumTextEventStream))
	if err != nil {
		d.fail(failureText(ctx, err), err)
		return
	}
	if res == nil || res.EventStream == nil {
		err := errors.New("openrouter: the response was not an event stream")
		d.fail(err.Error(), err)
		return
	}
	events := res.EventStream
	defer events.Close()
	for events.Next() {
		d.chunk(events.Value().Data)
	}
	if err := events.Err(); err != nil {
		d.fail(failureText(ctx, err), err)
		return
	}
	if !d.sawFinish {
		d.fail(failureText(ctx, errTruncated), errTruncated)
		return
	}
	d.finish()
}

// failureText is the text of a turn that failed with err: the abort text when
// the caller cancelled, otherwise err's text under the package prefix.
func failureText(caller context.Context, err error) string {
	if caller.Err() != nil {
		return abortText
	}
	if errors.Is(err, errTruncated) {
		return err.Error()
	}
	return "openrouter: " + err.Error()
}

// requestBody is the request body written so far: the model, the stream flags
// and the user and assistant text of the transcript.
type requestBody struct {
	Model         string        `json:"model"`
	Messages      []bodyMessage `json:"messages"`
	Stream        bool          `json:"stream"`
	StreamOptions streamOptions `json:"stream_options"`
}

type bodyMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// buildBody encodes the request for the model.
func buildBody(m core.Model, req core.Request) ([]byte, error) {
	b := requestBody{Model: m.ID, Messages: []bodyMessage{}, Stream: true, StreamOptions: streamOptions{IncludeUsage: true}}
	for _, msgs := range []core.Messages{req.Prefix, req.Messages} {
		for _, msg := range msgs {
			switch v := msg.(type) {
			case core.UserMessage:
				if text := v.Content.Text(); text != "" {
					b.Messages = append(b.Messages, bodyMessage{Role: "user", Content: text})
				}
			case core.AssistantMessage:
				if text := v.Content.Text(); text != "" {
					b.Messages = append(b.Messages, bodyMessage{Role: "assistant", Content: text})
				}
			}
		}
	}
	return json.Marshal(b)
}
