//go:build !windows

package rsyncx

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"sync/atomic"

	"github.com/gokrazy/rsync/rsyncd"
)

// Supported reports whether LocalDir transfers run on this platform.
const Supported = true

// built counts the servers NewServer has built.
var built atomic.Int64

// ServersBuilt is how many servers NewServer has built in this process.
func ServersBuilt() int64 { return built.Load() }

// Server is the rsync server of one sending side.
type Server struct {
	srv *rsyncd.Server
	mod rsyncd.Module
}

// NewServer builds the server that sends fsys: its one module serves fsys
// read-only. It is the only function in this repository that calls
// rsyncd.NewServer, and it always passes rsyncd.DontRestrict(): without it
// the library applies Landlock to the whole calling process, which in the
// local provider is the embedder.
func NewServer(fsys fs.FS) (*Server, error) {
	mod := rsyncd.Module{Name: "tree", FS: fsys}
	srv, err := rsyncd.NewServer([]rsyncd.Module{mod}, rsyncd.DontRestrict(), rsyncd.WithStderr(discard{}))
	if err != nil {
		return nil, err
	}
	built.Add(1)
	return &Server{srv: srv, mod: mod}, nil
}

// Serve runs one sending transfer over conn, configured by the server-side
// args (ServerArgs). The declared --delete is dropped: deletion is
// AgentKit's own.
func (s *Server) Serve(ctx context.Context, conn io.ReadWriteCloser, args []string) error {
	return contained(func() error {
		return s.srv.HandleConnArgs(ctx, rsyncd.NewConnection(conn, conn, "sandbox"), &s.mod, withoutDelete(args))
	})
}

// contained runs one transfer of the rsync library and turns a panic in it
// into an error. The library panics on some malformed input from the other
// side (a filter list with an impossible length, for one), and the other side
// is untrusted: on a Sync the host serves the agent's requests inside the
// embedder's process, so a malformed stream must fail the transfer, not end
// the process.
func contained(transfer func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("rsync: malformed stream from the other side: %v", r)
		}
	}()
	return transfer()
}

// discard swallows the library's log output.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
func (discard) Close() error                { return nil }

var _ io.WriteCloser = discard{}
