package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// tool is the proxy tool named name of sb.
func tool(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range tl {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no proxy tool %s", name)
	return core.Tool{}
}

// TS-19-18: Provider.Close destroys every open sandbox, joins the failures,
// closes the connection and is idempotent. 19-REQ-3.5.
func TestProviderClose_TS19_18(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	s1, err := p.Create(context.Background(), testSpec)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := p.Create(context.Background(), testSpec)
	if err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.destroyErr[s2.ID()] = status.Error(codes.Unavailable, "boom") })
	if err := p.Close(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Close = %v, want an error carrying boom", err)
	}
	got := f.destroyedIDs()
	if len(got) != 2 || !(got[0] == s1.ID() && got[1] == s2.ID() || got[0] == s2.ID() && got[1] == s1.ID()) {
		t.Errorf("the fake received Destroy for %v, want %s and %s", got, s1.ID(), s2.ID())
	}
	eventually(t, "the connection to close", func() bool { return f.openConns() == 0 })
	n := f.rpcCount()
	if err := p.Close(); err != nil || f.rpcCount() != n {
		t.Errorf("a second Close = %v with %d RPCs, want nil and none", err, f.rpcCount()-n)
	}
}

// TS-19-27: a GitRepo Sync checks liveness and the repository locally
// before calling SyncGit, and a success returns a zero SyncResult.
// 19-REQ-4.6.
func TestGitRepoSync_TS19_27(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	ctx := context.Background()
	sb := mustCreate(t, p)
	good := sandbox.GitRepo{URL: "https://example.test/r.git", Ref: "main", Depth: 1}
	r, err := sb.Sync(ctx, good)
	if r != (sandbox.SyncResult{}) || err != nil {
		t.Fatalf("Sync(GitRepo) = %+v, %v; want zeros and nil", r, err)
	}
	want := &sandboxpb.SyncGitRequest{Id: sb.ID(), Url: good.URL, Ref: "main", Depth: 1}
	if got := f.lastSyncGitRequest(); !proto.Equal(got, want) {
		t.Errorf("SyncGit carried %v, want %v", got, want)
	}

	n := f.n("SyncGit")
	bad := sandbox.GitRepo{URL: "ext::sh -c x"}
	if _, err := sb.Sync(ctx, bad); err == nil || err.Error() != gitclone.Check(bad).Error() {
		t.Errorf("an invalid repository gave %v, want gitclone.Check's %v", err, gitclone.Check(bad))
	}
	closed := mustCreate(t, p)
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.Sync(ctx, good); !errors.Is(err, sandbox.ErrClosed) {
		t.Errorf("a closed sandbox gave %v, want ErrClosed", err)
	}
	unavail := mustCreate(t, p)
	f.agent.SetCall(nil, status.Error(codes.Unavailable, "gone"), true)
	if r := tool(t, unavail, "read_file").Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Fatalf("a Call answered Unavailable gave %+v, want sandbox_unavailable", r)
	}
	if _, err := unavail.Sync(ctx, good); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("an unavailable sandbox gave %v, want ErrUnavailable", err)
	}
	if got := f.n("SyncGit"); got != n {
		t.Errorf("%d SyncGit RPCs were sent for the refused cases, want 0", got-n)
	}
}

// TS-19-29: a failed Destroy is returned by Close, is sent with a fresh
// 30-second context, and leaves the entry on the gateway. 19-REQ-4.7.
func TestCloseDestroyFails_TS19_29(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	sb, err := p.Create(context.Background(), testSpec)
	if err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.destroyErr[sb.ID()] = status.Error(codes.Unavailable, "backend busy") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sb.Close(ctx); err == nil || !strings.Contains(err.Error(), "backend busy") {
		t.Fatalf("Close = %v, want an error carrying backend busy", err)
	}
	d := f.lastDestroy()
	if d.id != sb.ID() || d.deadlineIn <= 25*time.Second {
		t.Errorf("Destroy %+v, want %s with a deadline over 25 s away", d, sb.ID())
	}
	if !f.has(sb.ID()) {
		t.Error("the fake no longer holds the sandbox")
	}
}

// A SyncGit that fails as a transport failure (the gateway no longer holds
// the sandbox, or the connection broke) makes the sandbox unavailable, as
// attach's own RPCs do: the Sync matches sandbox.ErrUnavailable, names the
// gateway, and later calls fail without an RPC. 19-REQ-10.4, 19-REQ-4.3.
func TestGitRepoSyncUnavailable(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	ctx := context.Background()
	sb := mustCreate(t, p)
	read := tool(t, sb, "read_file")
	f.set(func() { f.syncGit = fakeCreate{err: status.Error(codes.Unavailable, "gatewayd: no sandbox")} })
	_, err := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"})
	if !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "http://127.0.0.1") {
		t.Fatalf("Sync(GitRepo) = %v, want ErrUnavailable naming the gateway", err)
	}
	n := f.rpcCount()
	if r := read.Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after the failed SyncGit: %+v, want sandbox_unavailable", r)
	}
	if _, err := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("a second Sync(GitRepo) = %v, want ErrUnavailable", err)
	}
	if got := f.rpcCount(); got != n {
		t.Errorf("%d RPCs were sent after the sandbox became unavailable", got-n)
	}
}
