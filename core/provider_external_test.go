package core_test

import (
	"context"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// TS-12-9: a Stream on a context that is already done fails at once — with
// an error, or an error on the channel's last item.
func TestStreamOnADoneContextFails_TS12_9(t *testing.T) {
	m, _ := catalog.Lookup("claude-opus-5-5")
	clients := map[string]core.ProviderClient{
		"faux":      faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("never"))),
		"anthropic": anthropic.Provider(m, anthropic.Options{Getenv: func(string) string { return "" }}),
	}
	for name, client := range clients {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ch, err := client.Stream(ctx, core.Request{})
		if err != nil {
			continue
		}
		var last core.StreamEvent
		for ev := range ch {
			last = ev
		}
		if last.Err == nil {
			t.Errorf("%s: a stream on a cancelled context ended without an error", name)
		}
	}
}
