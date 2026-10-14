package gatewayd_test

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// hermetic ignores no global excludes, so a transfer depends on the tree
// alone.
var hermetic = tools.NoGlobalExcludes()

// tarDir is a tar stream of every directory and regular file under dir;
// symlinks are left out, so the archive is one any sandbox accepts.
func tarDir(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir || !(d.IsDir() || d.Type().IsRegular()) {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(h); err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// untarEntries maps each entry of a tar stream to its type and content.
func untarEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = string(h.Typeflag) + ":" + h.Linkname + ":" + string(body)
	}
}

// TS-19-69: LocalDir and Tar transfers through the gateway give the same
// workspace and destination trees as a direct local sandbox. 19-REQ-10.3.
func TestTransferParity_TS19_69(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	// pair is a fresh gateway sandbox and a fresh direct one, closed when
	// the seed is done so the gateway's capacity is never reached.
	pair := func(t *testing.T) []sandbox.Sandbox {
		t.Helper()
		gw, err := p.Create(ctx, unrestrictedSpec)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := local.Unisolated().Create(ctx, unrestrictedSpec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = gw.Close(ctx); _ = direct.Close(ctx) })
		return []sandbox.Sandbox{gw, direct}
	}
	for seed := range uint64(21) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { transferSeed(t, seed, pair) })
	}
}

// transferSeed compares one seed's transfers through the gateway and
// directly.
func transferSeed(t *testing.T, seed uint64, pair func(*testing.T) []sandbox.Sandbox) {
	ctx := context.Background()
	src := treegen.Gen(t, seed)
	var ws, dst [2]map[string]string
	for i, sb := range pair(t) {
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
			t.Fatalf("seed %d: LocalDir Sync: %v", seed, err)
		}
		ws[i] = treegen.Entries(t, sb.Spec().Workdir)
		out := filepath.Join(t.TempDir(), "dst")
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: out, Ignore: hermetic}); err != nil {
			t.Fatalf("seed %d: LocalDir Export: %v", seed, err)
		}
		dst[i] = treegen.Entries(t, out)
	}
	if !maps.Equal(ws[0], ws[1]) {
		t.Errorf("seed %d: the LocalDir workspaces differ", seed)
	}
	if !maps.Equal(dst[0], dst[1]) {
		t.Errorf("seed %d: the LocalDir exports differ", seed)
	}

	archive := tarDir(t, src)
	var tws, outs [2]map[string]string
	for i, sb := range pair(t) {
		if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(archive)}); err != nil {
			t.Fatalf("seed %d: Tar Sync: %v", seed, err)
		}
		tws[i] = treegen.Entries(t, sb.Spec().Workdir)
		var buf bytes.Buffer
		if err := sb.Export(ctx, ".", sandbox.TarOut{W: &buf}); err != nil {
			t.Fatalf("seed %d: TarOut Export: %v", seed, err)
		}
		outs[i] = untarEntries(t, buf.Bytes())
	}
	if !maps.Equal(tws[0], tws[1]) {
		t.Errorf("seed %d: the Tar workspaces differ", seed)
	}
	if len(outs[0]) == 0 || !maps.Equal(outs[0], outs[1]) {
		t.Errorf("seed %d: the TarOut archives differ", seed)
	}
}
