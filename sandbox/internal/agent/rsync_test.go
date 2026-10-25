//go:build !windows

package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func startFrame(s *sandboxpb.RsyncStart) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: s}}
}

var (
	syncStart   = startFrame(&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC})
	exportStart = startFrame(&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "dir"})
)

func argsFrame(a *sandboxpb.RsyncArgs) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Args{Args: a}}
}

// serverArgs are the agent's arguments this module produces: the agent
// receives on SYNC and sends operand on EXPORT.
func serverArgs(t *testing.T, del, export bool, operand string) []string {
	t.Helper()
	a, err := rsyncx.ServerArgs(rsyncx.ClientArgs(del), export, operand)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// drain reads st until it ends and returns the final error.
func drain(st sandboxpb.Agent_RsyncClient) error {
	for {
		if _, err := st.Recv(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// open starts a stream with start and returns it with the plan.
func open(t *testing.T, c sandboxpb.AgentClient, start *sandboxpb.RsyncFrame) (sandboxpb.Agent_RsyncClient, *sandboxpb.RsyncPlan, error) {
	t.Helper()
	st, err := c.Rsync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(start); err != nil {
		t.Fatal(err)
	}
	f, err := st.Recv()
	return st, f.GetPlan(), err
}

// serveSource serves src, filtered by rules, over st as the host does on SYNC.
func serveSource(t *testing.T, st sandboxpb.Agent_RsyncClient, src string, rules []string) *rsyncx.Conn {
	t.Helper()
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	srv, err := rsyncx.NewServer(rsyncx.Filtered(root, ".", rsyncx.NewMatcher(rules)))
	if err != nil {
		t.Fatal(err)
	}
	conn := rsyncx.HostConn(st)
	if err := srv.Serve(context.Background(), conn, serverArgs(t, false, true, ".")); err != nil {
		t.Fatalf("serving the source: %v", err)
	}
	_, _ = io.Copy(io.Discard, conn)
	return conn
}

// A SYNC writes the host's files, deletes what the host lacks unless a rule
// of either side protects it, and reports what changed.
func TestRsyncSyncReceivesDeletesAndCounts(t *testing.T) {
	ws := testutil.Tree(t, map[string]string{"a.txt": "a", "dir/b.txt": "b", "keep.txt": "k", ".gitignore": "*.log\n", "x.log": "l"})
	c := configured(t, ws)
	src := testutil.Tree(t, map[string]string{"a.txt": "hello", "new.txt": "n", "host-only.tmp": "t"})
	st, plan, err := open(t, c, startFrame(&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC, Delete: true}))
	if err != nil || !slices.Contains(plan.GetRules(), "x.log") {
		t.Fatalf("plan %v, %v; want the workspace's ignored x.log as a rule", plan, err)
	}
	rules := rsyncx.Combine(plan.GetRules(), []string{"keep.txt", ".gitignore", "host-only.tmp"})
	_ = st.Send(argsFrame(&sandboxpb.RsyncArgs{Args: serverArgs(t, true, false, "."), Rules: rules, Present: []string{"a.txt", "new.txt"}}))
	conn := serveSource(t, st, src, rules)
	d, ok := conn.Done()
	if !ok || d.Files != 2 || d.Bytes != 6 || d.Deleted != 2 {
		t.Errorf("done = %v, %v; want 2 files of 6 bytes and 2 deleted (dir/b.txt, dir)", d, ok)
	}
	want := []string{".gitignore", "a.txt", "keep.txt", "new.txt", "x.log"}
	if got, _ := os.ReadDir(ws); len(got) != len(want) {
		t.Errorf("the workspace holds %v, want %v", got, want)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(b) != "hello" {
		t.Errorf("a.txt = %q", b)
	}
	if err := drain(st); err != nil {
		t.Errorf("the stream ended with %v", err)
	}
}

// An EXPORT serves a filtered, read-only view of the export path, and done
// carries the transfer set when the host deletes.
func TestRsyncExportSendsTheFilteredPath(t *testing.T) {
	ws := testutil.Tree(t, map[string]string{"dir/b.txt": "b", "dir/skip.o": "o", "dir/.git/HEAD": "ref", "top.txt": "t"})
	c := configured(t, ws)
	st, plan, err := open(t, c, startFrame(&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "dir", Delete: true}))
	if err != nil || !slices.Contains(plan.GetRules(), ".git") {
		t.Fatalf("plan %v, %v; want the .git rule", plan, err)
	}
	_ = st.Send(argsFrame(&sandboxpb.RsyncArgs{Args: serverArgs(t, true, true, "dir/"), Rules: rsyncx.Combine(plan.GetRules(), []string{"skip.o"})}))
	dst := t.TempDir()
	conn := rsyncx.HostConn(st)
	if err := rsyncx.RunClient(context.Background(), conn, rsyncx.ClientArgs(false), dst); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, conn)
	if d, ok := conn.Done(); !ok || !slices.Equal(d.GetPresent(), []string{"b.txt"}) {
		t.Errorf("done = %v, %v; want the transfer set [b.txt]", d, ok)
	}
	if got, _ := os.ReadDir(dst); len(got) != 1 || got[0].Name() != "b.txt" {
		t.Errorf("the destination holds %v, want only b.txt", got)
	}
}

// A frame out of order is InvalidArgument naming its kind, and the agent
// stays live for the next stream.
func TestRsyncRefusesAFrameOutOfOrder(t *testing.T) {
	c := configured(t, testutil.Tree(t, map[string]string{"a.txt": "a", "dir/b.txt": "b"}))
	data := &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: []byte("x")}}
	args := argsFrame(&sandboxpb.RsyncArgs{Args: serverArgs(t, false, false, ".")})
	for _, k := range []struct {
		first, then []*sandboxpb.RsyncFrame
		kind        string
	}{
		{[]*sandboxpb.RsyncFrame{data}, nil, "data"},
		{[]*sandboxpb.RsyncFrame{args}, nil, "args"},
		{[]*sandboxpb.RsyncFrame{{}}, nil, "empty"},
		{[]*sandboxpb.RsyncFrame{syncStart}, []*sandboxpb.RsyncFrame{syncStart}, "start"},
		{[]*sandboxpb.RsyncFrame{syncStart}, []*sandboxpb.RsyncFrame{data}, "data"},
		{[]*sandboxpb.RsyncFrame{syncStart}, []*sandboxpb.RsyncFrame{args, args}, "args"},
	} {
		st, err := c.Rsync(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range k.first {
			_ = st.Send(f)
		}
		if k.then != nil {
			if f, err := st.Recv(); err != nil || f.GetPlan() == nil {
				t.Fatalf("no plan: %v, %v", f, err)
			}
			for _, f := range k.then {
				_ = st.Send(f)
			}
		}
		if err := drain(st); status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), k.kind) {
			t.Errorf("%s frame out of order: %v, want InvalidArgument naming it", k.kind, err)
		}
		if r := call(t, c, "list_files", `{"path":"."}`); !r.Ok {
			t.Errorf("list_files afterwards: %v", r)
		}
	}
}

// A second stream while one runs is FailedPrecondition, and the first
// completes with its own result.
func TestRsyncRunsOneStreamAtATime(t *testing.T) {
	ws := t.TempDir()
	c := configured(t, ws)
	s1, _, err := open(t, c, syncStart)
	if err != nil {
		t.Fatal(err)
	}
	_ = s1.Send(argsFrame(&sandboxpb.RsyncArgs{Args: serverArgs(t, false, false, ".")}))
	if _, p, err := open(t, c, syncStart); p != nil || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("second stream: plan %v, %v; want FailedPrecondition and no plan", p, err)
	}
	conn := serveSource(t, s1, testutil.Tree(t, map[string]string{"a.txt": "hello"}), nil)
	if d, ok := conn.Done(); !ok || d.Files != 1 {
		t.Errorf("done = %v, %v; want files 1", d, ok)
	}
	if err := drain(s1); err != nil {
		t.Errorf("stream 1 ended with %v, want OK", err)
	}
}

// An argument list this module would not produce is refused with
// InvalidArgument naming the token, before any server is built or any data
// moves.
func TestRsyncRefusesForeignArguments(t *testing.T) {
	c := configured(t, testutil.Tree(t, map[string]string{"a.txt": "a", "dir/b.txt": "b"}))
	sb, ex := serverArgs(t, false, false, "."), serverArgs(t, false, true, "dir/")
	plus := func(base []string, tok string) []string { return append(slices.Clone(base), tok) }
	withOp := func(op string) []string { a := slices.Clone(ex); a[len(a)-1] = op; return a }
	for _, k := range []struct {
		start *sandboxpb.RsyncFrame
		args  []string
		word  string
	}{
		{syncStart, plus(sb, "--frobnicate"), "--frobnicate"},
		{syncStart, plus(sb, "--include=x"), "--include=x"},
		{syncStart, plus(sb, "-c"), "-c"},
		{syncStart, plus(sb, "--sender"), "--sender"},
		{exportStart, slices.DeleteFunc(slices.Clone(ex), func(s string) bool { return s == "--sender" }), "--sender"},
		{exportStart, withOp("../etc"), "../etc"},
		{exportStart, withOp("other"), "other"},
	} {
		n0 := rsyncx.ServersBuilt()
		st, _, err := open(t, c, k.start)
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Send(argsFrame(&sandboxpb.RsyncArgs{Args: k.args}))
		var f *sandboxpb.RsyncFrame
		for err == nil {
			if f, err = st.Recv(); f.GetData() != nil {
				t.Errorf("%q: a data frame: the refused stream reached the transfer", k.args)
			}
		}
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), k.word) {
			t.Errorf("%q: %v, want InvalidArgument naming %s", k.args, err, k.word)
		}
		if rsyncx.ServersBuilt() != n0 {
			t.Errorf("%q: a server was built for a refused stream", k.args)
		}
	}
}

// An invalid start frame is refused before the plan, with InvalidArgument
// naming the field or NotFound for a missing export path.
func TestRsyncRefusesAnInvalidStart(t *testing.T) {
	ws := testutil.Tree(t, map[string]string{"file.txt": "f"})
	c := configured(t, ws)
	if err := os.Symlink("file.txt", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	export := func(path string, del bool) *sandboxpb.RsyncStart {
		return &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: path, Delete: del}
	}
	for _, k := range []struct {
		start *sandboxpb.RsyncStart
		code  codes.Code
		word  string
	}{
		{&sandboxpb.RsyncStart{}, codes.InvalidArgument, "direction"},
		{&sandboxpb.RsyncStart{Direction: 7}, codes.InvalidArgument, "direction"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC, Path: "x"}, codes.InvalidArgument, "path"},
		{export("../x", false), codes.InvalidArgument, "path"},
		{export("missing", false), codes.NotFound, "missing"},
		{export("link", false), codes.InvalidArgument, "symlink"},
		{export("file.txt", true), codes.InvalidArgument, "delete"},
	} {
		_, p, err := open(t, c, startFrame(k.start))
		if p != nil || status.Code(err) != k.code || !strings.Contains(status.Convert(err).Message(), k.word) {
			t.Errorf("start %v: plan %v, %v; want %v naming %s", k.start, p, err, k.code, k.word)
		}
	}
	if _, p, err := open(t, c, startFrame(export("file.txt", false))); err != nil || !p.GetFile() {
		t.Errorf("a file export: plan %v, %v; want File set", p, err)
	}
}

// A stream that finds the slot held by a cancelled stream waits for its
// release instead of being refused; a live holder is refused at once.
func TestRsyncSlotWaitsForACancelledHolder(t *testing.T) {
	var s rsyncSlot
	ctx1, cancel := context.WithCancel(context.Background())
	if !s.acquire(ctx1) {
		t.Fatal("the free slot was refused")
	}
	if s.acquire(context.Background()) {
		t.Fatal("a live holder's slot was granted")
	}
	cancel()
	waiter, giveUp := context.WithCancel(context.Background())
	gaveUp := make(chan bool)
	go func() { gaveUp <- s.acquire(waiter) }()
	giveUp()
	if <-gaveUp {
		t.Error("a waiter whose own context ended got the slot")
	}
	got := make(chan bool)
	go func() { got <- s.acquire(context.Background()) }()
	select {
	case ok := <-got:
		t.Fatalf("acquire returned %v before the cancelled holder released", ok)
	case <-time.After(50 * time.Millisecond):
	}
	s.release()
	if !<-got {
		t.Error("the slot was refused after the cancelled holder released")
	}
}
