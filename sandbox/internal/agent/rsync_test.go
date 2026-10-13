//go:build !windows

package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func startFrame(d sandboxpb.RsyncStart_Direction, path string) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: d, Path: path}}}
}

func argsFrame(args []string) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Args{Args: &sandboxpb.RsyncArgs{Args: args}}}
}

func dataOf(b []byte) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: b}}
}

// syncBase and exportBase are the agent's argument lists this module
// produces: the agent receives on SYNC and sends on EXPORT.
func syncBase(t *testing.T) []string {
	t.Helper()
	a, err := rsyncx.ServerArgs(rsyncx.ClientArgs(false), false, ".")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func exportBase(t *testing.T, operand string) []string {
	t.Helper()
	a, err := rsyncx.ServerArgs(rsyncx.ClientArgs(false), true, operand)
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

// recvPlan reads the first frame of st and returns its plan, or the error.
func recvPlan(st sandboxpb.Agent_RsyncClient) (*sandboxpb.RsyncPlan, error) {
	f, err := st.Recv()
	if err != nil {
		return nil, err
	}
	return f.GetPlan(), nil
}

// rsyncAgent serves an agent over a workspace holding a.txt, with tools
// configured.
func rsyncAgent(t *testing.T) (sandboxpb.AgentClient, string) {
	t.Helper()
	ws := t.TempDir()
	writeFile(t, ws, "a.txt", "a")
	writeFile(t, ws, "dir/b.txt", "b")
	c, _ := serveRaw(t, newAgent(t, ws, testEnv(t), time.Hour))
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	return c, ws
}

// TS-14-7: the agent refuses an out-of-order frame with InvalidArgument
// naming the frame kind, stops any rsync work it started, and stays live.
// 14-REQ-2.4.
func TestRsyncFrameOrder_TS14_7(t *testing.T) {
	c, _ := rsyncAgent(t)
	ctx := context.Background()
	sync := startFrame(sandboxpb.RsyncStart_SYNC, "")
	cases := []struct {
		frames  []*sandboxpb.RsyncFrame
		plan    int // index after which the plan frame is read
		kind    string
		comment string
	}{
		{[]*sandboxpb.RsyncFrame{dataOf([]byte("x"))}, -1, "data", "first frame data"},
		{[]*sandboxpb.RsyncFrame{argsFrame(syncBase(t))}, -1, "args", "first frame args"},
		{[]*sandboxpb.RsyncFrame{sync, sync}, -1, "start", "second start"},
		{[]*sandboxpb.RsyncFrame{sync, dataOf([]byte("x"))}, 0, "data", "data before args"},
		{[]*sandboxpb.RsyncFrame{sync, argsFrame(syncBase(t)), argsFrame(syncBase(t))}, 0, "args", "second args"},
	}
	for _, tc := range cases {
		st, err := c.Rsync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range tc.frames {
			if err := st.Send(f); err != nil {
				break
			}
			if i == tc.plan {
				if _, err := recvPlan(st); err != nil {
					t.Fatalf("%s: no plan: %v", tc.comment, err)
				}
			}
		}
		err = drain(st)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), tc.kind) {
			t.Errorf("%s: %v, want InvalidArgument naming %s", tc.comment, err, tc.kind)
		}
		if r := call(t, c, "list_files", `{"path":"."}`); !r.Ok {
			t.Errorf("%s: list_files afterwards: %v", tc.comment, r)
		}
	}
	st, _ := c.Rsync(ctx)
	_ = st.Send(sync)
	if p, err := recvPlan(st); err != nil || p == nil {
		t.Errorf("a new stream after the refusals: %v, %v; want a plan", p, err)
	}
	_ = st.CloseSend()
}

// TS-14-31: a second Rsync stream while one is running gets
// FailedPrecondition and the first completes with its own result.
// 14-REQ-6.2. Stream 1 is driven as the host drives it: a server sending a
// filtered source tree (D-3; docs/errata/14_sandbox_rsync_sync.md).
func TestRsyncOneStream_TS14_31(t *testing.T) {
	ctx := context.Background()
	ws := t.TempDir()
	c, _ := serveRaw(t, newAgent(t, ws, testEnv(t), time.Hour))
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	writeFile(t, src, "a.txt", "hello")

	s1, _ := c.Rsync(ctx)
	_ = s1.Send(startFrame(sandboxpb.RsyncStart_SYNC, ""))
	if _, err := recvPlan(s1); err != nil {
		t.Fatal(err)
	}
	_ = s1.Send(argsFrame(syncBase(t)))

	s2, _ := c.Rsync(ctx)
	_ = s2.Send(startFrame(sandboxpb.RsyncStart_SYNC, ""))
	if p, err := recvPlan(s2); p != nil || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("second stream: plan %v, %v; want FailedPrecondition and no plan", p, err)
	}

	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	srv, err := rsyncx.NewServer(rsyncx.Filtered(root, ".", rsyncx.NewMatcher(nil)))
	if err != nil {
		t.Fatal(err)
	}
	hostArgs, _ := rsyncx.ServerArgs(rsyncx.ClientArgs(false), true, ".")
	conn := rsyncx.HostConn(s1)
	if err := srv.Serve(ctx, conn, hostArgs); err != nil {
		t.Fatalf("serving the source: %v", err)
	}
	_, _ = io.Copy(io.Discard, conn)
	if d, ok := conn.Done(); !ok || d.Files != 1 {
		t.Errorf("done = %v, %v; want files 1", d, ok)
	}
	if err := drain(s1); err != nil {
		t.Errorf("stream 1 ended with %v, want OK", err)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "a.txt")); err != nil || string(b) != "hello" {
		t.Errorf("workspace a.txt: %q, %v", b, err)
	}
	s3, _ := c.Rsync(ctx)
	_ = s3.Send(startFrame(sandboxpb.RsyncStart_SYNC, ""))
	if p, err := recvPlan(s3); err != nil || p == nil {
		t.Errorf("third stream: %v, %v", p, err)
	}
	_ = s3.CloseSend()
	if r := call(t, c, "list_files", `{"path":"."}`); !r.Ok {
		t.Error(r)
	}
}

// TS-14-32: an args frame with an unknown option, a known option outside the
// allowlist, a mismatched sender flag or a foreign path operand is refused
// with InvalidArgument naming it, before any server is built.
// 14-REQ-6.3, 14-REQ-6.4, 14-REQ-6.5.
func TestRsyncArgChecks_TS14_32(t *testing.T) {
	c, ws := rsyncAgent(t)
	ctx := context.Background()
	snap := treeSnapshot(t, ws)
	sb, ex := syncBase(t), exportBase(t, "dir/")
	for _, opt := range []string{"--include=x", "--filter=- x", "-c"} {
		if _, err := rsyncx.ParseArgs(append(slices.Clone(sb), opt)); err != nil {
			t.Errorf("the library's parser does not recognise %s: %v", opt, err)
		}
	}
	// --remove-source-files is not an option this library knows at all; it is
	// refused all the same, by the parser check.
	withOp := func(base []string, op string) []string {
		a := slices.Clone(base)
		a[len(a)-1] = op
		return a
	}
	without := func(base []string, tok string) []string {
		return slices.DeleteFunc(slices.Clone(base), func(s string) bool { return s == tok })
	}
	type tc struct {
		start *sandboxpb.RsyncFrame
		args  []string
		word  string
	}
	syncStart, exportStart := startFrame(sandboxpb.RsyncStart_SYNC, ""), startFrame(sandboxpb.RsyncStart_EXPORT, "dir")
	cases := []tc{
		{syncStart, append(slices.Clone(sb), "--frobnicate"), "--frobnicate"},
		{syncStart, append(slices.Clone(sb), "--remove-source-files"), "--remove-source-files"},
		{syncStart, append(slices.Clone(sb), "--include=x"), "--include=x"},
		{syncStart, append(slices.Clone(sb), "--filter=- x"), "--filter=- x"},
		{syncStart, append(slices.Clone(sb), "-c"), "-c"},
		{syncStart, append(slices.Clone(sb), "--sender"), "--sender"},
		{exportStart, without(ex, "--sender"), "--sender"},
		{exportStart, withOp(ex, "../etc"), "../etc"},
		{exportStart, withOp(ex, "other"), "other"},
	}
	for _, k := range cases {
		n0 := rsyncx.ServersBuilt()
		st, _ := c.Rsync(ctx)
		_ = st.Send(k.start)
		if _, err := recvPlan(st); err != nil {
			t.Fatalf("%v: %v", k.args, err)
		}
		_ = st.Send(argsFrame(k.args))
		err := drain(st)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), k.word) {
			t.Errorf("%q: %v, want InvalidArgument naming %s", k.args, err, k.word)
		}
		if rsyncx.ServersBuilt() != n0 {
			t.Errorf("%q: a server was built for a refused stream", k.args)
		}
		if r := call(t, c, "list_files", `{"path":"."}`); !r.Ok {
			t.Errorf("%q: list_files afterwards: %v", k.args, r)
		}
	}
	if got := treeSnapshot(t, ws); !slices.Equal(got, snap) {
		t.Errorf("the workspace changed: %v -> %v", snap, got)
	}

	// The unmodified baselines are accepted: SYNC reaches the data phase (the
	// agent's client speaks first), EXPORT builds its server.
	st, _ := c.Rsync(ctx)
	_ = st.Send(syncStart)
	_, _ = recvPlan(st)
	_ = st.Send(argsFrame(sb))
	if f, err := st.Recv(); err != nil || f.GetData() == nil {
		t.Errorf("SYNC baseline: %v, %v; want a data frame", f, err)
	}
	_ = st.CloseSend()
	_ = drain(st)
	for _, op := range []string{"dir/", "dir"} {
		n0 := rsyncx.ServersBuilt()
		st, _ = c.Rsync(ctx)
		_ = st.Send(exportStart)
		_, _ = recvPlan(st)
		_ = st.Send(argsFrame(exportBase(t, op)))
		deadline := time.Now().Add(2 * time.Second)
		for rsyncx.ServersBuilt() == n0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if rsyncx.ServersBuilt() != n0+1 {
			t.Errorf("EXPORT baseline with operand %q built no server", op)
		}
		_ = st.CloseSend()
		_ = drain(st)
	}
}

// treeSnapshot lists every entry under dir with its size and mtime.
func treeSnapshot(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, fmt.Sprintf("%s %v %v %d", rel, fi.Mode(), fi.ModTime(), fi.Size()))
		return nil
	})
	return out
}

// TS-14-30: an invalid start frame is refused before plan with
// InvalidArgument naming the field, or NotFound for a missing export path.
// 14-REQ-6.1. The host half is in sandbox/rsync_export_test.go.
func TestRsyncStartChecks_TS14_30(t *testing.T) {
	c, ws := rsyncAgent(t)
	writeFile(t, ws, "file.txt", "f")
	if err := os.Symlink("file.txt", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	snap := treeSnapshot(t, ws)
	cases := []struct {
		start *sandboxpb.RsyncStart
		code  codes.Code
		word  string
	}{
		{&sandboxpb.RsyncStart{}, codes.InvalidArgument, "direction"},
		{&sandboxpb.RsyncStart{Direction: 7}, codes.InvalidArgument, "direction"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC, Path: "x"}, codes.InvalidArgument, "path"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "../x"}, codes.InvalidArgument, "path"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "missing"}, codes.NotFound, "missing"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "link"}, codes.InvalidArgument, "path"},
		{&sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "file.txt", Delete: true}, codes.InvalidArgument, "delete"},
	}
	for _, k := range cases {
		st, _ := c.Rsync(context.Background())
		_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: k.start}})
		f, err := st.Recv()
		if f != nil || status.Code(err) != k.code || !strings.Contains(status.Convert(err).Message(), k.word) {
			t.Errorf("start %v: frame %v, %v; want %v naming %s", k.start, f, err, k.code, k.word)
		}
		if r := call(t, c, "list_files", `{"path":"."}`); !r.Ok {
			t.Error(r)
		}
	}
	if got := treeSnapshot(t, ws); !slices.Equal(got, snap) {
		t.Error("the workspace changed")
	}
}
