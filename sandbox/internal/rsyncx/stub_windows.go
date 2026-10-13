//go:build windows

package rsyncx

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"runtime"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Supported reports whether LocalDir transfers run on this platform: not on
// Windows, where the rsync library's support is unverified.
const Supported = false

// unsupported is the error every stub returns.
func unsupported() error {
	return fmt.Errorf("%w: LocalDir transfers on %s", sandbox.ErrUnsupported, runtime.GOOS)
}

// ServersBuilt is always 0 on this platform.
func ServersBuilt() int64 { return 0 }

// Server is not available on this platform.
type Server struct{}

// NewServer refuses on this platform.
func NewServer(fs.FS) (*Server, error) { return nil, unsupported() }

// Serve refuses on this platform.
func (*Server) Serve(context.Context, io.ReadWriteCloser, []string) error { return unsupported() }

// ServerArgs refuses on this platform.
func ServerArgs([]string, bool, string) ([]string, error) { return nil, unsupported() }

// RunClient refuses on this platform.
func RunClient(context.Context, io.ReadWriteCloser, []string, string) error { return unsupported() }

// ParseArgs refuses on this platform.
func ParseArgs([]string) (Parsed, error) { return Parsed{}, unsupported() }

// CheckServerArgs refuses on this platform.
func CheckServerArgs([]string, Direction, string) error { return unsupported() }
