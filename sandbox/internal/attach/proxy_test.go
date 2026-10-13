package attach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

var aborted = core.ErrResult("aborted", "Operation aborted")

// TS-13-41: a proxy sends its input bytes verbatim and maps a well-formed
// response onto a ToolResult with UseNumber Data and decoded Metadata.
// 13-REQ-7.1.
func TestProxyMapsResponse_TS13_41(t *testing.T) {
	f := fakeagent.New()
	data := []byte(`{"n":12345678901234567890,"s":"x"}`)
	f.SetCall(&sandboxpb.CallResponse{Ok: true, Data: data, Detail: "d", Metadata: []byte(`{"exit_code":0}`), Text: "t"}, nil, false)
	p := proxies(t, attachFake(t, f, Config{}))["read_file"]
	in := []byte(`{ "path" :  "a.txt" }`)
	r := p.Execute(context.Background(), in)
	if !bytes.Equal(f.LastInput(), in) {
		t.Errorf("the agent received %q, want %q verbatim", f.LastInput(), in)
	}
	if !r.OK || r.Detail != "d" || r.Text != "t" || r.Terminate || r.Blocks != nil {
		t.Errorf("result %+v", r)
	}
	if _, isNum := r.Data["n"].(json.Number); !isNum {
		t.Errorf("Data[n] is %T, want json.Number", r.Data["n"])
	}
	if got, _ := json.Marshal(r.Data); !bytes.Equal(got, data) {
		t.Errorf("re-encoded Data %s, want the agent's %s", got, data)
	}
	if r.Metadata == nil || r.Metadata.ExitCode == nil || *r.Metadata.ExitCode != 0 {
		t.Errorf("Metadata %+v, want exit code 0", r.Metadata)
	}
	f.SetCall(&sandboxpb.CallResponse{Ok: true}, nil, false)
	if r := p.Execute(context.Background(), in); r.Data != nil || r.Metadata != nil {
		t.Errorf("empty data and metadata gave %+v", r)
	}
}

// TS-13-42: an input larger than MaxMessageBytes returns arguments_too_large
// without an RPC and leaves the sandbox live. 13-REQ-7.2.
func TestProxyTooLarge_TS13_42(t *testing.T) {
	f := fakeagent.New()
	p := proxies(t, attachFake(t, f, Config{}))["write_file"]
	big := append(append([]byte(`{"content":"`), bytes.Repeat([]byte("x"), sandbox.MaxMessageBytes+1-14)...), `"}`...)
	n := f.Count("Call")
	if r := p.Execute(context.Background(), big); r.OK || r.Error != "arguments_too_large" || f.Count("Call") != n {
		t.Fatalf("oversized input: %s %s, %d RPCs", r.Error, r.Detail, f.Count("Call")-n)
	}
	if r := p.Execute(context.Background(), []byte(`{}`)); f.Count("Call") != n+1 || r.Error == "sandbox_unavailable" {
		t.Fatalf("the next call: %+v, %d RPCs", r, f.Count("Call")-n)
	}
}

// TS-13-43: malformed Data or Metadata from the agent is a transport failure
// returning sandbox_unavailable naming the field. 13-REQ-7.3.
func TestProxyMalformedResponse_TS13_43(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	cases := []struct {
		field string
		resp  *sandboxpb.CallResponse
	}{
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(`[1,2]`)}},
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(`{"a":1,"a":2}`)}},
		{"data", &sandboxpb.CallResponse{Ok: true, Data: []byte(deep)}},
		{"metadata", &sandboxpb.CallResponse{Ok: true, Metadata: []byte(`{"exit_code":`)}},
		{"metadata", &sandboxpb.CallResponse{Ok: true, Metadata: []byte(`{"exit_code":"x"}`)}},
	}
	for _, tc := range cases {
		f := fakeagent.New()
		sb := attachFake(t, f, Config{})
		p := proxies(t, sb)["read_file"]
		f.SetCall(tc.resp, nil, false)
		r := p.Execute(context.Background(), []byte(`{}`))
		if r.OK || r.Error != "sandbox_unavailable" || !strings.Contains(r.Detail, tc.field) {
			t.Errorf("%s %q: %+v, want sandbox_unavailable naming %s", tc.field, tc.resp, r, tc.field)
		}
		if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%s: Tools afterwards = %v, want ErrUnavailable", tc.field, err)
		}
	}
}

// TS-13-44: a host context that ends before or during a call returns the
// aborted result at once, cancels the RPC and leaves the sandbox live.
// 13-REQ-7.4.
func TestProxyCancelled_TS13_44(t *testing.T) {
	f := fakeagent.New()
	p := proxies(t, attachFake(t, f, Config{}))["execute"]
	f.SetBlockCall(true)

	c, cancel := context.WithCancel(context.Background())
	cancel()
	n := f.Count("Call")
	if r := p.Execute(c, []byte(`{}`)); !reflect.DeepEqual(r, aborted) || f.Count("Call") != n {
		t.Fatalf("pre-cancelled: %+v, %d RPCs", r, f.Count("Call")-n)
	}

	c2, cancel2 := context.WithCancel(context.Background())
	got := make(chan core.ToolResult, 1)
	go func() { got <- p.Execute(c2, []byte(`{}`)) }()
	<-f.Entered()
	time.Sleep(100 * time.Millisecond)
	cancel2()
	select {
	case r := <-got:
		if !reflect.DeepEqual(r, aborted) {
			t.Fatalf("mid-call cancel: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the cancelled call did not return within 1s")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !errors.Is(f.CallCtxErr(), context.Canceled) {
		if time.Now().After(deadline) {
			t.Fatalf("the agent saw %v, want context.Canceled", f.CallCtxErr())
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.SetBlockCall(false)
	if r := p.Execute(context.Background(), []byte(`{}`)); r.Error == "sandbox_unavailable" {
		t.Fatalf("the sandbox did not stay live: %+v", r)
	}
}

// TS-13-45: a transport failure returns sandbox_unavailable naming the cause
// and every later call on any proxy fails the same way without an RPC.
// 13-REQ-7.5.
func TestProxyStickyUnavailable_TS13_45(t *testing.T) {
	f := fakeagent.New()
	ps := proxies(t, attachFake(t, f, Config{}))
	rf, ex := ps["read_file"], ps["execute"]
	f.SetCall(nil, status.Error(codes.Unavailable, "pipe broke"), true)
	n := f.Count("Call")
	r := rf.Execute(context.Background(), []byte(`{}`))
	if r.OK || r.Error != "sandbox_unavailable" || !strings.Contains(r.Detail, "pipe broke") {
		t.Fatalf("first call: %+v", r)
	}
	for _, p := range []core.Tool{ex, rf} {
		if r := p.Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" {
			t.Errorf("%s afterwards: %+v", p.Name, r)
		}
	}
	if f.Count("Call") != n+1 {
		t.Errorf("%d Call RPCs, want exactly 1 (no retry, no reconnect)", f.Count("Call")-n)
	}
}

// TS-13-46: after Close a proxy returns sandbox_unavailable with detail
// "sandbox closed", and an agent's unknown_tool answer is passed through.
// 13-REQ-7.6, 13-REQ-7.7.
func TestProxyClosedAndUnknownTool_TS13_46(t *testing.T) {
	f := fakeagent.New()
	sb := attachFake(t, f, Config{})
	p := proxies(t, sb)["read_file"]
	f.SetCall(&sandboxpb.CallResponse{Error: "unknown_tool", Detail: "available: a, b"}, nil, false)
	if r := p.Execute(context.Background(), []byte(`{}`)); r.OK || r.Error != "unknown_tool" || r.Detail != "available: a, b" {
		t.Fatalf("unknown_tool: %+v", r)
	}
	// The tool set is configured, so Tools answers from the host without an
	// RPC; passing the state gate is what shows the sandbox is still live.
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
		t.Errorf("the sandbox did not stay live: %v", err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := f.Count("Call")
	if r := p.Execute(context.Background(), []byte(`{}`)); r.Error != "sandbox_unavailable" || r.Detail != "sandbox closed" || f.Count("Call") != c {
		t.Fatalf("after Close: %+v, %d RPCs", r, f.Count("Call")-c)
	}
}
