package gatewayd_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// localSandbox is a direct local sandbox, closed at the end of the test.
func localSandbox(t *testing.T) sandbox.Sandbox {
	t.Helper()
	sb, err := local.Unisolated().Create(context.Background(), unrestrictedSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// localDir is the directory of a local sandbox, the parent of its
// workspace.
func localDir(sb sandbox.Sandbox) string { return filepath.Dir(sb.Spec().Workdir) }

// TS-19-72 (smoke, 19-PATH-1): an embedder dials an in-process gateway over
// local, creates a sandbox and reads a file through the proxy as from a
// local sandbox. 19-REQ-4.4, 19-REQ-7.1.
func TestSmokeReadFile_TS19_72(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	p, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: env.Listener.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	sb := create(t, p, unrestrictedSpec)
	infos, err := p.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].ID != sb.ID() {
		t.Fatalf("List = %v, %v; want the one sandbox %s", infos, err, sb.ID())
	}
	direct := localSandbox(t)
	archive := tarOf(t, map[string]string{"notes/a.txt": "line one\nline two\n"})
	for _, s := range []sandbox.Sandbox{sb, direct} {
		if _, err := s.Sync(ctx, sandbox.Tar{R: bytes.NewReader(archive)}); err != nil {
			t.Fatal(err)
		}
	}
	in := []byte(`{"path":"notes/a.txt"}`)
	got := toolOf(t, sb, "read_file").Execute(ctx, in)
	want := toolOf(t, direct, "read_file").Execute(ctx, in)
	if !got.OK || !reflect.DeepEqual(got.Data, want.Data) || got.Text != want.Text {
		t.Errorf("read_file through the gateway = %+v, direct = %+v", got, want)
	}
	if mine, _ := env.Dial(t, gwtest.TokenB).List(ctx); len(mine) != 0 {
		t.Errorf("another token sees %v", mine)
	}
}

// TS-19-73 (smoke, 19-PATH-2): a LocalDir Sync and Export ride the relayed
// Rsync stream and match a direct sandbox, and the completed SYNC takes the
// GitRepo slot. 19-REQ-10.3.
func TestSmokeLocalDir_TS19_73(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	src := t.TempDir()
	for name, body := range map[string]string{".gitignore": "*.tmp\n", "a/b/c.txt": "c", "d.txt": "d", "x.tmp": "t",
		"big.bin": strings.Repeat("z", 3<<20)} {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gw, direct := create(t, p, unrestrictedSpec), localSandbox(t)
	var ws, dst [2]map[string]string
	for i, sb := range []sandbox.Sandbox{gw, direct} {
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
		ws[i] = treegen.Entries(t, sb.Spec().Workdir)
		out := filepath.Join(t.TempDir(), "dst")
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: out, Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
		dst[i] = treegen.Entries(t, out)
	}
	if len(ws[0]) == 0 || !maps.Equal(ws[0], ws[1]) || !maps.Equal(dst[0], dst[1]) {
		t.Errorf("the gateway's trees differ from the direct sandbox's:\nworkspace %v\nvs %v", ws[0], ws[1])
	}
	_, err := gw.Sync(ctx, repo)
	if err == nil || err.Error() != "GitRepo must be the first Sync and come before Tools" {
		t.Errorf("GitRepo after a LocalDir Sync = %v, want the slot refusal", err)
	}
}

// TS-19-74 (smoke, 19-PATH-3): Provider.Close destroys every open sandbox on
// the gateway and the local backend deletes their directories.
// 19-REQ-3.5.
func TestSmokeProviderClose_TS19_74(t *testing.T) {
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted, Logger: logger})
	p, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: env.Listener.Dial})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var ids, dirs []string
	for range 2 {
		sb, err := p.Create(ctx, unrestrictedSpec)
		if err != nil {
			t.Fatal(err)
		}
		ids, dirs = append(ids, sb.ID()), append(dirs, localDir(sb))
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	for i, id := range ids {
		if !gone(dirs[i]) {
			t.Errorf("sandbox %s's directory is still there", id)
		}
		if r := destroyReason(logs, id); r != "client" {
			t.Errorf("sandbox %s was destroyed with reason %q, want client", id, r)
		}
	}
	if infos, err := env.Dial(t, gwtest.Token).List(ctx); err != nil || len(infos) != 0 {
		t.Errorf("a new provider lists %v, %v; want nothing", infos, err)
	}
}

// TS-19-75 (smoke, 19-PATH-4): a 2-second-TTL sandbox expires on the
// gateway, its backend is removed, and the client sees sandbox_unavailable.
// 19-REQ-6.7, 19-REQ-10.4.
func TestSmokeExpiry_TS19_75(t *testing.T) {
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted, Logger: logger})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb := create(t, p, sandbox.Spec{Network: unrestrictedSpec.Network, TTL: 2 * time.Second})
	read := toolOf(t, sb, "read_file")
	dir := localDir(sb)
	time.Sleep(3 * time.Second)
	if !gone(dir) || destroyReason(logs, sb.ID()) == "" {
		t.Errorf("after 3 s the directory is gone: %v; destroy line: %q", gone(dir), destroyReason(logs, sb.ID()))
	}
	if r := read.Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after expiry: %+v, want sandbox_unavailable", r)
	}
	for range 2 {
		if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)}); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("Sync after expiry: %v, want ErrUnavailable", err)
		}
	}
}
