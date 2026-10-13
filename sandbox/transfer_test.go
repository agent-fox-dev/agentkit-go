package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// entry is one tar entry to build.
type entry struct {
	name, body, link string
	flag             byte
}

func file(name, body string) entry { return entry{name: name, body: body, flag: tar.TypeReg} }
func dir(name string) entry        { return entry{name: name, flag: tar.TypeDir} }
func symlink(name, target string) entry {
	return entry{name: name, link: target, flag: tar.TypeSymlink}
}

func tarOf(t *testing.T, es ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range es {
		h := &tar.Header{Typeflag: e.flag, Name: e.name, Linkname: e.link, Mode: 0o644, Size: int64(len(e.body))}
		if e.flag == tar.TypeDir {
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// isolate points TMPDIR at a fresh directory, so local's root is private.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
}

// newLocal creates a local sandbox closed at the end of the test.
func newLocal(t *testing.T) sandbox.Sandbox {
	t.Helper()
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// fakeSandbox attaches to a fake agent.
func fakeSandbox(t *testing.T, f *fakeagent.Agent) sandbox.Sandbox {
	t.Helper()
	sb, err := attach.Dial(context.Background(), f.Serve(), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()); f.Stop() })
	return sb
}

// rawAgent serves a real agent over ws and returns a raw client.
func rawAgent(t *testing.T, ws string) sandboxpb.AgentClient {
	t.Helper()
	a, err := agent.New(agent.Config{Workdir: ws, Env: []string{"TMPDIR=" + t.TempDir()}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	hostEnd, agentEnd := net.Pipe()
	go func() { _ = a.Serve(agentEnd) }()
	cc, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return hostEnd, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return sandboxpb.NewAgentClient(cc)
}

func tool(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no proxy %s", name)
	return core.Tool{}
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// readTar returns the headers and contents of a tar stream by name, in
// order.
func readTar(t *testing.T, b []byte) ([]*tar.Header, map[string]string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	var hs []*tar.Header
	bodies := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return hs, bodies
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		hs = append(hs, h)
		bodies[h.Name] = string(body)
	}
}

// TS-13-49: the Goal 7 hostile entries are refused both when a tar is
// synced into a local sandbox and when the host validates an export.
// 13-REQ-8.2.
func TestHostileEntriesBothWays_TS13_49(t *testing.T) {
	isolate(t)
	hostile := []struct {
		entry string
		tar   []entry
	}{
		{"../x", []entry{file("../x", "x")}},
		{"/etc/x", []entry{file("/etc/x", "x")}},
		{"l", []entry{symlink("l", "../../outside"), file("l/f", "x")}},
		{"s", []entry{symlink("s", "/etc")}},
		{"s", []entry{symlink("s", "../..")}},
		{"h", []entry{file("a.txt", "a"), {name: "h", link: "a.txt", flag: tar.TypeLink}}},
		{"c", []entry{{name: "c", flag: tar.TypeChar}}},
		{"b", []entry{{name: "b", flag: tar.TypeBlock}}},
		{"p", []entry{{name: "p", flag: tar.TypeFifo}}},
	}
	f := fakeagent.New()
	fsb := fakeSandbox(t, f)
	for _, tc := range hostile {
		blob := tarOf(t, tc.tar...)
		_, err := newLocal(t).Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(blob)})
		if !errors.Is(err, sandbox.ErrUnsafeArchive) || !strings.Contains(err.Error(), `"`+tc.entry+`"`) {
			t.Errorf("Sync %s: %v, want ErrUnsafeArchive naming it", tc.entry, err)
		}
		f.SetExport(blob, nil)
		if err := fsb.Export(context.Background(), "", sandbox.TarOut{W: &bytes.Buffer{}}); !errors.Is(err, sandbox.ErrUnsafeArchive) {
			t.Errorf("Export %s: %v, want ErrUnsafeArchive", tc.entry, err)
		}
	}
	ok := tarOf(t, dir("sub/"), file("sub/file", "f"), symlink("in", "sub/file"))
	if _, err := newLocal(t).Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(ok)}); err != nil {
		t.Errorf("Sync of an inside symlink, a directory and a file: %v", err)
	}
	f.SetExport(ok, nil)
	if err := fsb.Export(context.Background(), "", sandbox.TarOut{W: &bytes.Buffer{}}); err != nil {
		t.Errorf("Export of an inside symlink, a directory and a file: %v", err)
	}
}

// TS-13-51: Sync creates missing parents, reports files and content bytes,
// and Replace empties the path while keeping or creating it. 13-REQ-8.4.
func TestSyncParentsCountsAndReplace_TS13_51(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ws := sb.Agent().Workdir
	for rel, body := range map[string]string{"keep.txt": "k", "sub/old.txt": "o"} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	res, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("a/b/c.txt", "12345"), file("d.txt", "abc"), dir("e/")))})
	if err != nil || res.Files != 2 || res.Bytes != 8 {
		t.Fatalf("Sync = %+v, %v; want Files 2, Bytes 8", res, err)
	}
	for _, p := range []string{"a/b/c.txt", "d.txt", "e"} {
		if !exists(filepath.Join(ws, p)) {
			t.Errorf("%s missing", p)
		}
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("new.txt", "n"))), Path: "sub", Replace: true}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(ws, "sub")); err != nil || !fi.IsDir() || exists(filepath.Join(ws, "sub/old.txt")) ||
		!exists(filepath.Join(ws, "sub/new.txt")) || !exists(filepath.Join(ws, "keep.txt")) {
		t.Error("Replace into sub did not empty it and keep it")
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("x.txt", "x"))), Path: "fresh", Replace: true}); err != nil || !exists(filepath.Join(ws, "fresh/x.txt")) {
		t.Errorf("Replace into an absent path: %v", err)
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("more.txt", "m"))), Path: "sub"}); err != nil ||
		!exists(filepath.Join(ws, "sub/new.txt")) || !exists(filepath.Join(ws, "sub/more.txt")) {
		t.Errorf("Sync without Replace: %v", err)
	}
}

// TS-13-52: a refused entry stops the sync with InvalidArgument on the
// agent and ErrUnsafeArchive on the host, keeping earlier entries and the
// sandbox live. 13-REQ-8.5.
func TestRefusedEntryStopsSync_TS13_52(t *testing.T) {
	blob := tarOf(t, file("a.txt", "a"), file("../evil", "e"), file("z.txt", "z"))
	ws := t.TempDir()
	stream, err := rawAgent(t, ws).SyncTar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Start{Start: &sandboxpb.TarStart{}}})
	_ = stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: blob}})
	_, err = stream.CloseAndRecv()
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "../evil") {
		t.Errorf("raw SyncTar = %v, want InvalidArgument naming ../evil", err)
	}
	if !exists(filepath.Join(ws, "a.txt")) || exists(filepath.Join(ws, "z.txt")) {
		t.Error("raw: want a.txt kept and z.txt never written")
	}

	isolate(t)
	sb := newLocal(t)
	_, err = sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(blob)})
	if !errors.Is(err, sandbox.ErrUnsafeArchive) || !strings.Contains(err.Error(), "../evil") {
		t.Errorf("Sync = %v, want ErrUnsafeArchive naming ../evil", err)
	}
	lws := sb.Agent().Workdir
	if !exists(filepath.Join(lws, "a.txt")) || exists(filepath.Join(lws, "z.txt")) {
		t.Error("local: want a.txt kept and z.txt never written")
	}
	if r := tool(t, sb, "read_file").Execute(context.Background(), []byte(`{"path":"a.txt"}`)); !r.OK {
		t.Errorf("read_file after the refusal: %+v", r)
	}
}

// TS-13-55: exporting a missing path is NotFound naming the path, and a
// refused exported entry stops with ErrUnsafeArchive writing nothing from it
// onward. 13-REQ-8.8.
func TestExportMissingAndRefused_TS13_55(t *testing.T) {
	stream, err := rawAgent(t, t.TempDir()).ExportTar(context.Background(), &sandboxpb.ExportRequest{Path: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.NotFound {
		t.Errorf("raw ExportTar(missing) = %v, want NotFound", err)
	}
	isolate(t)
	sb := newLocal(t)
	if err := sb.Export(context.Background(), "missing", sandbox.TarOut{W: io.Discard}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("Export(missing) = %v, want an error naming it", err)
	}
	f := fakeagent.New()
	fsb := fakeSandbox(t, f)
	f.SetExport(tarOf(t, file("good.txt", "g"), symlink("bad", "/etc/passwd"), file("after.txt", "a")), nil)
	var buf bytes.Buffer
	if err := fsb.Export(context.Background(), "", sandbox.TarOut{W: &buf}); !errors.Is(err, sandbox.ErrUnsafeArchive) {
		t.Errorf("Export of a refused entry = %v, want ErrUnsafeArchive", err)
	}
	if hs, _ := readTar(t, buf.Bytes()); len(hs) != 1 || hs[0].Name != "good.txt" {
		t.Errorf("the destination holds %v, want only good.txt", hs)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
		t.Errorf("local Tools afterwards: %v", err)
	}
	n := f.Count("Tools")
	_, _ = fsb.Tools(context.Background(), sandbox.ToolOptions{})
	if f.Count("Tools") != n+1 {
		t.Error("the fake-backed sandbox did not stay live")
	}
}

// TS-13-73 (smoke, 13-PATH-5): an embedder exports a subtree written by a
// proxy tool and finds the file in the validated tar. 13-REQ-8.7.
func TestSmokeExportProxyOutput_TS13_73(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ln")
	}
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	if r := tool(t, sb, "write_file").Execute(ctx, []byte(`{"path":"out/result.txt","content":"done"}`)); !r.OK {
		t.Fatalf("write_file: %+v", r)
	}
	if r := tool(t, sb, "execute").Execute(ctx, []byte(`{"command":"ln -s result.txt out/link"}`)); !r.OK {
		t.Fatalf("execute: %+v", r)
	}
	var buf bytes.Buffer
	if err := sb.Export(ctx, "out", sandbox.TarOut{W: &buf}); err != nil {
		t.Fatal(err)
	}
	hs, bodies := readTar(t, buf.Bytes())
	sawLink := false
	for _, h := range hs {
		if strings.HasPrefix(h.Name, "out/") {
			t.Errorf("entry %q is not relative to out", h.Name)
		}
		if h.Name == "link" {
			sawLink = h.Typeflag == tar.TypeSymlink && h.Linkname == "result.txt"
		}
	}
	if bodies["result.txt"] != "done" || !sawLink {
		t.Errorf("export: %v, result.txt %q", hs, bodies["result.txt"])
	}
}
