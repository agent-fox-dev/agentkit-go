// Package local is a sandbox provider with no isolation, for tests and
// development. It runs the agent service in the embedder's own process,
// with the embedder's privileges, on the host filesystem and network, over
// the same stdio connection adapter agentkit-sandboxd uses, so the
// production protocol and transport are exercised. It is not a sandbox.
package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// provider is the local provider; it has no state.
type provider struct{}

// Unisolated returns the local provider. Its name says what it is: tools run
// in the embedder's process with its privileges.
func Unisolated() sandbox.Provider { return provider{} }

// Capabilities: no isolation, unrestricted network only, no resource
// bounds, no image, no workdir choice, and no Landlock: the agent runs in the
// embedder's process, which nothing may confine.
func (provider) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{
		Provider:  "local",
		Isolation: sandbox.IsolationNone,
		Network:   []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		Notes:     "local provides no isolation: tools run in the embedder's process with its privileges, on the host filesystem and network",
	}
}

// root is where local sandboxes live.
func root() string { return filepath.Join(os.TempDir(), "agentkit-sandbox-local") }

// Create checks spec, makes <os.TempDir()>/agentkit-sandbox-local/<id>/
// with private workspace/, home/ and tmp/ directories and a marker, starts
// the agent in-process over two os.Pipe pairs and attaches to it.
func (p provider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	eff, err := sandbox.CheckSpec(p.Capabilities(), spec)
	if err != nil {
		return nil, err
	}
	if len(spec.Provider) > 0 {
		k := slices.Sorted(maps.Keys(spec.Provider))[0]
		return nil, fmt.Errorf("sandbox: provider %q does not define Spec.Provider option %q", "local", k)
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root(), id)
	if err := makeDirs(dir); err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	if err := writeMarker(dir, marker{PID: os.Getpid(), Labels: eff.Labels, Created: time.Now(), TTLNs: int64(eff.TTL)}); err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	a, err := agent.New(agent.Config{Workdir: filepath.Join(dir, "workspace"), Env: agentEnv(eff.Env, dir), TTL: eff.TTL,
		Landlock: sandbox.LandlockInfo{Note: "in-process agent (local provider): no confinement"}})
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	hostConn, agentConn, err := pipes()
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = a.Serve(agentConn)
	}()
	cut := func() error { return errors.Join(hostConn.Close(), agentConn.Close()) }
	localtest.Register(id, cut)
	destroy := func(ctx context.Context) error {
		_ = cut()
		select {
		case <-served:
		case <-ctx.Done():
		}
		localtest.Deregister(id)
		return os.RemoveAll(dir)
	}
	return attach.Dial(ctx, hostConn, attach.Config{ID: id, Spec: eff, Destroy: destroy, Provider: "local"})
}

// newID is 16 random lowercase hex characters.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// makeDirs creates dir and its workspace/, home/ and tmp/, each 0700 whatever
// the umask.
func makeDirs(dir string) error {
	if err := os.MkdirAll(root(), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	for _, sub := range []string{"workspace", "home", "tmp"} {
		p := filepath.Join(dir, sub)
		if err := os.Mkdir(p, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// pipes joins a host and an agent connection with two os.Pipe pairs.
func pipes() (host, agent net.Conn, err error) {
	r1, w1, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	r2, w2, err := os.Pipe()
	if err != nil {
		return nil, nil, errors.Join(err, r1.Close(), w1.Close())
	}
	return stdioconn.New(r2, w1), stdioconn.New(r1, w2), nil
}

// Reap removes every sandbox directory under local's root whose marker
// names a process that is not running, and returns their ids. Where there
// is no liveness probe it removes nothing.
func (provider) Reap(context.Context) ([]string, error) {
	if !canProbe {
		return nil, nil
	}
	ents, err := os.ReadDir(root())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		dir := filepath.Join(root(), e.Name())
		m, err := readMarker(dir)
		if !e.IsDir() || err != nil || alive(m.PID) {
			continue
		}
		if err := os.RemoveAll(dir); err == nil {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
