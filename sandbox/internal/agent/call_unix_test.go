//go:build unix

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// waitFile waits up to 5s for path to hold a line and returns it.
func waitFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && bytes.HasSuffix(b, []byte("\n")) {
			return strings.TrimSpace(string(b))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not appear", path)
	return ""
}

// assertChildGone reads the pid in pidFile and waits up to 5s for that
// process to be gone.
func assertChildGone(t *testing.T, pidFile string) {
	t.Helper()
	pid, err := strconv.Atoi(waitFile(t, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("process %d is still running", pid)
}

// TS-13-19: Call runs each request concurrently with the RPC context,
// marshals Data and Metadata with encoding/json, and a cancelled RPC kills
// the command's process group. 13-REQ-3.5.
func TestCallConcurrentAndCancellable_TS13_19(t *testing.T) {
	ws := t.TempDir()
	writeFile(t, ws, "f.txt", "one\ntwo\n")
	env := testEnv(t)
	c, _ := serveRaw(t, newAgent(t, ws, env, time.Hour))
	ctx := context.Background()
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}

	in := `{"path":"f.txt"}`
	r := call(t, c, "read_file", in)
	w, _ := workspace.Open(ws, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	all, _ := tools.For(w, tools.Options{Env: env})
	var local []byte
	var localMD []byte
	for _, tl := range all {
		if tl.Name == "read_file" {
			res := tl.Execute(ctx, json.RawMessage(in))
			local, _ = json.Marshal(res.Data)
			if !res.Metadata.IsEmpty() {
				localMD, _ = json.Marshal(res.Metadata)
			}
		}
	}
	if !bytes.Equal(r.Data, local) || !bytes.Equal(r.Metadata, localMD) {
		t.Errorf("read_file data %s metadata %s, want %s and %s", r.Data, r.Metadata, local, localMD)
	}

	start := time.Now()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if r := call(t, c, "execute", `{"command":"sleep 1"}`); !r.Ok {
				t.Errorf("sleep 1: %s %s", r.Error, r.Detail)
			}
		})
	}
	wg.Wait()
	if d := time.Since(start); d >= 3*time.Second {
		t.Errorf("four parallel sleep 1 calls took %v: calls are serialised", d)
	}

	cctx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() {
		_, err := c.Call(cctx, &sandboxpb.CallRequest{Name: "execute",
			Input: []byte(`{"command":"sleep 60 & echo $! > child.pid; wait"}`)})
		errc <- err
	}()
	waitFile(t, filepath.Join(ws, "child.pid"))
	cancel()
	if err := <-errc; status.Code(err) != codes.Canceled {
		t.Errorf("cancelled call returned %v, want Canceled", err)
	}
	assertChildGone(t, filepath.Join(ws, "child.pid"))
}

// TS-13-22: at the TTL the agent stops serving, cancels in-flight calls and
// Serve returns an error naming the TTL. 13-REQ-3.8.
func TestTTLStopsServing_TS13_22(t *testing.T) {
	ws := t.TempDir()
	a := newAgent(t, ws, testEnv(t), 300*time.Millisecond)
	c, errc := serveRaw(t, a)
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	callErr := make(chan error, 1)
	go func() {
		_, err := c.Call(context.Background(), &sandboxpb.CallRequest{Name: "execute",
			Input: []byte(`{"command":"sleep 60 & echo $! > child.pid; wait"}`)})
		callErr <- err
	}()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "300ms") {
			t.Fatalf("Serve returned %v, want an error naming 300ms", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within 5s of a 300ms TTL")
	}
	select {
	case err := <-callErr:
		if err == nil {
			t.Error("the in-flight call ended without an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight call did not end")
	}
	assertChildGone(t, filepath.Join(ws, "child.pid"))
}
