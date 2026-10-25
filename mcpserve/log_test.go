package mcpserve

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The records are exactly these, so none carries the token, an argument, a
// result's text or Detail, a tool's own error text or a panic's text.
func TestEachCallRefusalAndFailureIsLoggedOnceWithoutWorkspaceContentOrTheToken(t *testing.T) {
	var token string
	echoing := tool("t", func(_ context.Context, in json.RawMessage) core.ToolResult {
		if strings.Contains(string(in), "fail") {
			return core.ToolResult{Error: "a sentence, not a code", Text: "TEXT", Detail: "DETAIL"}
		}
		return core.ToolResult{OK: true, Text: "TEXT echoing the token " + token}
	})
	logs := &capture{}
	s, _ := serverOf(t, Options{Logger: slog.New(logs)}, echoing)
	ep := serveLoop(t, s)
	token = ep.Token
	imported := dial(t, ep)
	imported[0].Execute(context.Background(), json.RawMessage(`{"text":"ARGS"}`))
	imported[0].Execute(context.Background(), json.RawMessage(`{"text":"fail ARGS"}`))
	post(ep.URL, "Bearer "+token[:40]+"zzzzzzzz", "{}")
	post(ep.URL, "Bearer "+token, strings.Repeat("a", int(wire.Defaults().MaxMessageBytes)+1))
	post(ep.URL, "Bearer "+token, `{"ARGS":1,"ARGS":"TEXT"}`)
	c := serveStdio(t, context.Background(), s)
	c.send(initFrame, readyFrame, call("1", `{"name":"t","arguments":{"ARGS":1,"ARGS":2}}`))
	<-c.done //nolint:errcheck // the session ends on the bad frame; its log is what is checked
	old := newSDKHandler
	newSDKHandler = func(*sdk.Server) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("PANIC") })
	}
	bad := serveLoop(t, s)
	newSDKHandler = old
	post(bad.URL, "Bearer "+bad.Token, initFrame)
	testutil.Eventually(t, "the server error's record", func() bool { return strings.Contains(logs.text(), "http server error") })

	vary := regexp.MustCompile(`duration=\S+|:\d+`)
	if got := vary.ReplaceAllString(logs.text(), "_"); got != `INFO tool call tool=t ok=true _
INFO tool call tool=t ok=false _ error=other
WARN request refused peer=127.0.0.1_ reason=missing or wrong token
WARN request refused peer=127.0.0.1_ reason=body over the size limit
WARN request refused peer=127.0.0.1_ reason=body rejected: duplicate_key
WARN stdio session ended with an error reason=frame rejected: duplicate_key
WARN http server error` {
		t.Errorf("the log is\n%s", got)
	}
}
