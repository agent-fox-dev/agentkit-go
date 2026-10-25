//go:build !windows

package rsyncx

import (
	"context"
	"io"
	"slices"

	"github.com/gokrazy/rsync/rsyncclient"
)

// newClient is the only function in this repository that calls
// rsyncclient.New. It always passes rsyncclient.DontRestrict(): as a
// receiver the library otherwise applies Landlock to the whole process.
func newClient(args []string, sender bool) (*rsyncclient.Client, error) {
	if sender {
		return rsyncclient.New(withoutDelete(args), rsyncclient.DontRestrict(), rsyncclient.WithStderr(discard{}), rsyncclient.WithSender())
	}
	return rsyncclient.New(withoutDelete(args), rsyncclient.DontRestrict(), rsyncclient.WithStderr(discard{}))
}

// ServerArgs are the server-side arguments that pair with a client using
// clientArgs: with serverSends the server is the sender (--sender), and
// operand names what it sends ("." for the root, "dir/" for a directory's
// contents, a file's path for the file). A --delete in clientArgs is kept as
// the declared intent.
func ServerArgs(clientArgs []string, serverSends bool, operand string) ([]string, error) {
	c, err := newClient(clientArgs, !serverSends)
	if err != nil {
		return nil, err
	}
	args := c.ServerCommandOptions(operand)
	if slices.Contains(clientArgs, "--delete") {
		// Before the operands, which ServerCommandOptions ends with.
		args = slices.Insert(args, len(args)-2, "--delete")
	}
	return args, nil
}

// RunClient runs the receiving client of one transfer over conn, writing
// into the directory dest. The declared --delete in args is dropped.
func RunClient(ctx context.Context, conn io.ReadWriteCloser, args []string, dest string) error {
	c, err := newClient(args, false)
	if err != nil {
		return err
	}
	return contained(func() error {
		_, err := c.Run(ctx, conn, []string{dest})
		return err
	})
}

// withoutDelete is args without --delete.
func withoutDelete(args []string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--delete" })
}
