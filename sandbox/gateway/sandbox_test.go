package gateway

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Every RPC carries the bearer token, and only Agent RPCs the sandbox id.
func TestRPCsCarryTheTokenAndAgentRPCsTheSandboxID(t *testing.T) {
	f := newFakeGW(t, nil)
	emptyTar := make([]byte, 1024)
	f.agent.SetExport(emptyTar, nil)
	p := f.dial(t)
	ctx := context.Background()
	sb, err := p.Create(ctx, testSpec)
	if err != nil {
		t.Fatal(err)
	}
	if sb.Spec().Image != testSpec.Image || sb.Agent().ProtocolMajor != sandbox.ProtocolMajor {
		t.Errorf("Spec() = %+v, Agent() = %+v", sb.Spec(), sb.Agent())
	}
	_ = tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"x"}`))
	_, e1 := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)})
	e2 := sb.Export(ctx, ".", sandbox.TarOut{W: &bytes.Buffer{}})
	_, e3 := p.List(ctx)
	if err := errors.Join(e1, e2, e3, sb.Close(ctx)); err != nil {
		t.Fatal(err)
	}
	var agentRPCs []string
	f.with(func() {
		for _, r := range f.rpcs {
			if got := r.md.Get("authorization"); !slices.Equal(got, []string{"Bearer " + testToken}) {
				t.Errorf("%s carried authorization %q", r.method, got)
			}
			id := r.md.Get(SandboxIDKey)
			if m, ok := strings.CutPrefix(r.method, "/agentkit.sandbox.v1.Agent/"); ok {
				agentRPCs = append(agentRPCs, m)
				if !slices.Equal(id, []string{sb.ID()}) {
					t.Errorf("%s carried %s %q, want [%s]", r.method, SandboxIDKey, id, sb.ID())
				}
			} else if id != nil {
				t.Errorf("the Gateway RPC %s carried %s %q", r.method, SandboxIDKey, id)
			}
		}
		if !slices.Equal(f.destroys, []string{sb.ID()}) {
			t.Errorf("Close sent Destroy for %v, want [%s]", f.destroys, sb.ID())
		}
	})
	for _, m := range []string{"Hello", "Tools", "Call", "SyncTar", "ExportTar"} {
		if !slices.Contains(agentRPCs, m) {
			t.Errorf("no Agent %s RPC was recorded among %v", m, agentRPCs)
		}
	}
}

// A GitRepo Sync is the gateway's SyncGit, sent only for a live sandbox and a
// valid repository; a success reports zeros, one cut short by its context
// leaves the sandbox live, and a transport failure makes it unavailable, as
// attach's own RPCs do, so that later calls fail with no RPC.
func TestAGitRepoSyncIsTheGatewaysSyncGit(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	ctx := context.Background()
	sb := mustCreate(t, p)
	good := sandbox.GitRepo{URL: "https://example.test/r.git", Ref: "main", Depth: 1}
	if r, err := sb.Sync(ctx, good); r != (sandbox.SyncResult{}) || err != nil {
		t.Fatalf("Sync(GitRepo) = %+v, %v; want zeros and nil", r, err)
	}
	f.with(func() {
		if want := (&sandboxpb.SyncGitRequest{Id: sb.ID(), Url: good.URL, Ref: "main", Depth: 1}); !proto.Equal(f.lastSync, want) {
			t.Errorf("SyncGit carried %v, want %v", f.lastSync, want)
		}
	})
	bad := sandbox.GitRepo{URL: "ext::sh -c x"}
	if _, err := sb.Sync(ctx, bad); err == nil || err.Error() != gitclone.Check(bad).Error() || f.count("/SyncGit") != 1 {
		t.Errorf("an invalid repository gave %v, want gitclone.Check's %v and no RPC", err, gitclone.Check(bad))
	}
	closed := mustCreate(t, p)
	_ = closed.Close(ctx)
	if _, err := closed.Sync(ctx, good); !errors.Is(err, sandbox.ErrClosed) || !errors.Is(attach.Live(closed), sandbox.ErrClosed) {
		t.Errorf("a closed sandbox gave %v, want ErrClosed", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sb.Sync(cancelled, good); !errors.Is(err, context.Canceled) || attach.Live(sb) != nil {
		t.Errorf("a cancelled Sync(GitRepo) = %v, live %v; want context.Canceled and a live sandbox", err, attach.Live(sb))
	}
	read := tool(t, sb, "read_file")
	f.with(func() { f.syncGitErr = status.Error(codes.Unavailable, "gatewayd: no sandbox") })
	if _, err := sb.Sync(ctx, good); !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "http://127.0.0.1") {
		t.Fatalf("Sync(GitRepo) = %v, want ErrUnavailable naming the gateway", err)
	}
	n := f.count("")
	if r := read.Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after the failed SyncGit: %+v, want sandbox_unavailable", r)
	}
	if _, err := sb.Sync(ctx, good); !errors.Is(err, sandbox.ErrUnavailable) || f.count("") != n {
		t.Errorf("a later Sync(GitRepo) = %v after %d RPCs, want ErrUnavailable and none", err, f.count("")-n)
	}
}

// A failed Destroy is Close's error; it is sent with a fresh 30-second
// context, whatever Close's.
func TestCloseReturnsAFailedDestroy(t *testing.T) {
	f := newFakeGW(t, nil)
	sb, err := f.dial(t).Create(context.Background(), testSpec)
	if err != nil {
		t.Fatal(err)
	}
	f.with(func() { f.destroyErr[sb.ID()] = status.Error(codes.Unavailable, "backend busy") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sb.Close(ctx); err == nil || !strings.Contains(err.Error(), "backend busy") {
		t.Fatalf("Close = %v, want an error carrying backend busy", err)
	}
	f.with(func() {
		if !slices.Equal(f.destroys, []string{sb.ID()}) || f.destroyIn <= 25*time.Second {
			t.Errorf("Destroy for %v with a deadline %v away, want [%s] and about 30 s", f.destroys, f.destroyIn, sb.ID())
		}
	})
}
