package agent

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-25: the agent returns Config.Landlock in Hello field for field, and
// attach fills AgentInfo.Landlock from it. 15-REQ-4.5.
func TestHelloLandlock_TS15_25(t *testing.T) {
	l := sandbox.LandlockInfo{ABI: 3, KernelABI: 5, TCP: false, Note: "n",
		Writable: []string{"/w1", "/w2", "/dev/null"}, ReadOnly: []string{"/usr", "/proc"}}
	build := func(info sandbox.LandlockInfo) *Agent {
		a, err := New(Config{Workdir: t.TempDir(), TTL: time.Hour, Landlock: info})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	c, _ := serveRaw(t, build(l))
	h, err := c.Hello(context.Background(), &sandboxpb.HelloRequest{})
	if err != nil {
		t.Fatal(err)
	}
	r := h.GetLandlock()
	if r.GetAbi() != 3 || r.GetKernelAbi() != 5 || r.GetTcp() || r.GetNote() != "n" ||
		!reflect.DeepEqual(r.GetWritable(), l.Writable) || !reflect.DeepEqual(r.GetReadOnly(), l.ReadOnly) {
		t.Errorf("HelloResponse.landlock = %v, want %+v", r, l)
	}
	attached := func(a *Agent) sandbox.AgentInfo {
		hostEnd, agentEnd := net.Pipe()
		go func() { _ = a.Serve(agentEnd) }()
		sb, err := attach.Dial(context.Background(), hostEnd, attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sb.Close(context.Background()) })
		return sb.Agent()
	}
	if got := attached(build(l)).Landlock; !reflect.DeepEqual(got, l) {
		t.Errorf("Agent().Landlock = %+v, want %+v", got, l)
	}
	if got := attached(build(sandbox.LandlockInfo{})); got.ProtocolMinor != 2 || !reflect.DeepEqual(got.Landlock, sandbox.LandlockInfo{}) {
		t.Errorf("a zero-report 1.%d agent: Agent().Landlock = %+v, want the zero value", got.ProtocolMinor, got.Landlock)
	}
}
