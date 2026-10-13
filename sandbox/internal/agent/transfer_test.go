package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// tarOf builds a tar of the given files, in order.
func tarOf(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: f[0], Mode: 0o644, Size: int64(len(f[1]))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// syncTar sends blob through a raw SyncTar stream.
func syncTar(t *testing.T, c sandboxpb.AgentClient, path string, replace bool, blob []byte) (*sandboxpb.SyncResponse, error) {
	t.Helper()
	s, err := c.SyncTar(context.Background())
	if err != nil {
		return nil, err
	}
	if err := s.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Start{Start: &sandboxpb.TarStart{Path: path, Replace: replace}}}); err != nil {
		return nil, err
	}
	for len(blob) > 0 {
		n := min(len(blob), 1<<20)
		if err := s.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: blob[:n]}}); err != nil {
			break
		}
		blob = blob[n:]
	}
	return s.CloseAndRecv()
}

// TS-13-21: a successful SyncTar rebuilds the configured tool set so
// find_symbol sees synced files, while an in-flight call finishes on the old
// set. 13-REQ-3.7.
func TestSyncRebuildsTools_TS13_21(t *testing.T) {
	c, _ := serveRaw(t, newAgent(t, "", testEnv(t), time.Hour))
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	if r := call(t, c, "find_symbol", `{"name":"NewThing"}`); strings.Contains(r.Text+string(r.Data), "thing.go") {
		t.Fatalf("find_symbol matched before the sync: %s", r.Text)
	}
	inflight := make(chan *sandboxpb.CallResponse, 1)
	go func() { inflight <- call(t, c, "execute", `{"command":"sleep 1; echo done"}`) }()
	time.Sleep(100 * time.Millisecond)
	res, err := syncTar(t, c, "", false, tarOf(t, [2]string{"pkg/thing.go", "package pkg\n\nfunc NewThing() {}\n"}))
	if err != nil || res.Files != 1 {
		t.Fatalf("SyncTar: %v, %v", res, err)
	}
	if r := call(t, c, "find_symbol", `{"name":"NewThing"}`); !r.Ok || !strings.Contains(r.Text+string(r.Data), "pkg/thing.go") {
		t.Fatalf("find_symbol after the sync: %s %s %s", r.Error, r.Text, r.Data)
	}
	if r := <-inflight; !r.Ok || !strings.Contains(r.Text, "done") {
		t.Fatalf("the in-flight call: %v", r)
	}
}
