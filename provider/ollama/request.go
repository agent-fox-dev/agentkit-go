package ollama

import (
	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// chatRequest is the request body: the model as written, the messages, the tools,
// streaming, no truncation of the input, the thinking level, and the options (the
// context length the model loads at, the output bound and the sampling the
// request sets). It fails, before any request is made, when a tool's schema is
// one the SDK's tool type cannot carry. keep_alive, format and images are never
// sent.
func (c *Client) chatRequest(req core.Request) (*api.ChatRequest, error) {
	tools, err := chatTools(req)
	if err != nil {
		return nil, err
	}
	stream, truncate := true, false
	return &api.ChatRequest{
		Model:    c.model.ID,
		Messages: c.chatMessages(req),
		Stream:   &stream,
		Truncate: &truncate,
		Tools:    tools,
		Think:    thinkFor(c.model, req.Effort),
		Options:  c.options(req),
	}, nil
}

// options are the model options of a request. num_ctx is sent on every request,
// because Ollama applies it only when the model loads and the window the agent
// prunes against must be the one the model loads at; the others only when there
// is a value for them. An explicit temperature of 0 is sent.
func (c *Client) options(req core.Request) map[string]any {
	opts := map[string]any{"num_ctx": c.contextLength()}
	if n, ok := numPredict(c.model, req); ok {
		opts["num_predict"] = n
	}
	if req.Temperature != nil {
		opts["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		opts["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		opts["stop"] = req.StopSequences
	}
	return opts
}

// contextLength is num_ctx: the model's window, else the catalog's default for an
// Ollama model with none.
func (c *Client) contextLength() int {
	if c.model.ContextWindow > 0 {
		return c.model.ContextWindow
	}
	return catalog.OllamaDefaultContextWindow
}

// numPredict is num_predict, the bound on the output: the requested bound when it
// is positive and not above the model's cap, else the cap, else nothing. With no
// cap a positive request stands.
func numPredict(m core.Model, req core.Request) (int, bool) {
	asked := 0
	if req.MaxTokens != nil {
		asked = *req.MaxTokens
	}
	switch {
	case asked > 0 && (m.MaxOutputTokens <= 0 || asked <= m.MaxOutputTokens):
		return asked, true
	case m.MaxOutputTokens > 0:
		return m.MaxOutputTokens, true
	}
	return 0, false
}
