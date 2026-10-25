package attach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

var aborted = core.ErrResult("aborted", "Operation aborted")

// A proxy sends its input verbatim and maps the answer: Data with
// json.Number, so it re-encodes to the agent's bytes, and decoded Metadata;
// an agent's failed result such as unknown_tool passes through.
func TestProxyMapsTheAgentsAnswer(t *testing.T) {
	f := fakeagent.New()
	data := []byte(`{"n":12345678901234567890,"s":"x"}`)
	f.SetCall(&sandboxpb.CallResponse{Ok: true, Data: data, Detail: "d", Metadata: []byte(`{"exit_code":0}`), Text: "t"}, nil, false)
	sb := attachFake(t, f, Config{})
	p := proxies(t, sb)["read_file"]
	in := []byte(`{ "path" :  "a.txt" }`)
	r := p.Execute(context.Background(), in)
	if !bytes.Equal(f.LastInput(), in) || !r.OK || r.Detail != "d" || r.Text != "t" || r.Terminate || r.Blocks != nil {
		t.Errorf("the agent received %q; result %+v", f.LastInput(), r)
	}
	if got, _ := json.Marshal(r.Data); !bytes.Equal(got, data) {
		t.Errorf("re-encoded Data %s, want the agent's %s", got, data)
	}
	if r.Metadata == nil || r.Metadata.ExitCode == nil || *r.Metadata.ExitCode != 0 {
		t.Errorf("Metadata %+v, want exit code 0", r.Metadata)
	}
	f.SetCall(&sandboxpb.CallResponse{Error: "unknown_tool", Detail: "available: a, b"}, nil, false)
	if r := p.Execute(context.Background(), in); r.OK || r.Error != "unknown_tool" || r.Detail != "available: a, b" || r.Data != nil || r.Metadata != nil {
		t.Errorf("unknown_tool: %+v", r)
	}
	if err := Live(sb); err != nil {
		t.Errorf("Live = %v", err)
	}
}

func TestProxyRefusesAnInputOverTheMessageLimitWithoutAnRPC(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	p := proxies(t, sb)["write_file"]
	big := append(append([]byte(`{"content":"`), bytes.Repeat([]byte("x"), sandbox.MaxMessageBytes+1-14)...), `"}`...)
	if r := p.Execute(context.Background(), big); r.OK || r.Error != "arguments_too_large" || f.Count("Call") != 0 {
		t.Fatalf("oversized input: %s %s, %d RPCs", r.Error, r.Detail, f.Count("Call"))
	}
	isLive(t, sb)
}

// Malformed Data or Metadata is a transport failure naming the field.
func TestProxyTreatsAMalformedAnswerAsATransportFailure(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	for _, c := range []struct {
		field string
		resp  *sandboxpb.CallResponse
	}{
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(`[1,2]`)}},
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(`{"a":1,"a":2}`)}},
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(deep)}},
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(`{"a":1} x`)}},
		{"metadata", &sandboxpb.CallResponse{Ok: true, Metadata: []byte(deep)}},
		{"metadata", &sandboxpb.CallResponse{Ok: true, Metadata: []byte(`{"exit_code":"x"}`)}},
	} {
		f := fakeagent.New()
		sb := attachFake(t, f, Config{})
		p := proxies(t, sb)["read_file"]
		f.SetCall(c.resp, nil, false)
		if r := p.Execute(context.Background(), []byte(`{}`)); r.OK || r.Error != "sandbox_unavailable" || !strings.Contains(r.Detail, c.field) {
			t.Errorf("%s %q: %+v, want sandbox_unavailable naming %s", c.field, c.resp, r, c.field)
		}
		if err := Live(sb); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%s: Live afterwards = %v, want ErrUnavailable", c.field, err)
		}
	}
}

// A host context that ends before or during a call gives the aborted result,
// cancels the RPC, and leaves the sandbox live.
func TestProxyAbortsWithTheHostContext(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	p := proxies(t, sb)["execute"]
	f.SetBlockCall(true)
	c, cancel := context.WithCancel(context.Background())
	cancel()
	if r := p.Execute(c, []byte(`{}`)); !reflect.DeepEqual(r, aborted) || f.Count("Call") != 0 {
		t.Fatalf("pre-cancelled: %+v, %d RPCs", r, f.Count("Call"))
	}
	c2, cancel2 := context.WithCancel(context.Background())
	got := make(chan core.ToolResult, 1)
	go func() { got <- p.Execute(c2, []byte(`{}`)) }()
	<-f.Entered()
	cancel2()
	if r := <-got; !reflect.DeepEqual(r, aborted) {
		t.Fatalf("mid-call cancel: %+v", r)
	}
	testutil.Eventually(t, "the agent to see the call cancelled", func() bool { return errors.Is(f.CallCtxErr(), context.Canceled) })
	f.SetBlockCall(false)
	isLive(t, sb)
}

// Proxy calls run concurrently: three blocking calls are in the agent at once.
func TestProxyCallsRunConcurrently(t *testing.T) {
	f := fakeagent.New()
	p := proxies(t, attachFake(t, f, Config{}))["execute"]
	f.SetBlockCall(true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan core.ToolResult, 3)
	for range 3 {
		go func() { done <- p.Execute(ctx, []byte(`{}`)) }()
	}
	for range 3 {
		<-f.Entered()
	}
	cancel()
	for range 3 {
		<-done
	}
}

// A transport failure is sandbox_unavailable naming the cause, and every
// later call on any proxy fails the same way without an RPC; after Close the
// detail is "sandbox closed".
func TestProxyFailsForGoodAfterATransportFailureOrClose(t *testing.T) {
	f := fakeagent.New()
	ps := proxies(t, attachFake(t, f, Config{}))
	f.SetCall(nil, status.Error(codes.Unavailable, "pipe broke"), true)
	if r := ps["read_file"].Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" || !strings.Contains(r.Detail, "pipe broke") {
		t.Fatalf("first call: %+v", r)
	}
	for _, p := range []core.Tool{ps["execute"], ps["read_file"]} {
		if r := p.Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" {
			t.Errorf("%s afterwards: %+v", p.Name, r)
		}
	}
	if f.Count("Call") != 1 {
		t.Errorf("%d Call RPCs, want exactly 1 (no retry, no reconnect)", f.Count("Call"))
	}
	g := fakeagent.New()
	sb := attachFake(t, g, Config{})
	p := proxies(t, sb)["read_file"]
	_ = sb.Close(context.Background())
	if r := p.Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" || r.Detail != "sandbox closed" || g.Count("Call") != 0 {
		t.Errorf("after Close: %+v, %d RPCs", r, g.Count("Call"))
	}
}
