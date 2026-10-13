package attach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// rsyncCheck refuses a LocalDir transfer this host or agent cannot run,
// before any walk or stream: a state other than live, Windows, or an agent
// below protocol 1.1.
func (s *sb) rsyncCheck() error {
	if err := s.gate(); err != nil {
		return err
	}
	if !rsyncx.Supported {
		return fmt.Errorf("%w: LocalDir transfers on %s", sandbox.ErrUnsupported, runtime.GOOS)
	}
	if s.agent.ProtocolMajor == 1 && s.agent.ProtocolMinor < 1 {
		return fmt.Errorf("%w: LocalDir needs agent protocol 1.1, agent speaks %d.%d",
			sandbox.ErrUnsupported, s.agent.ProtocolMajor, s.agent.ProtocolMinor)
	}
	return nil
}

// resolveDir resolves a LocalDir path once: a relative path joins the
// working directory at call time, and symlinks are evaluated.
func resolveDir(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("sandbox: LocalDir path %q is empty", p)
	}
	abs := p
	if !filepath.IsAbs(abs) {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("sandbox: LocalDir path %q: %v", p, err)
		}
		abs = filepath.Join(wd, p)
	}
	return filepath.EvalSymlinks(abs)
}

// syncLocalDir fills the workspace from a host directory: the host serves a
// filtered view of the directory, the agent's client receives it, and the
// agent reports what changed (D-3, docs/errata/14_sandbox_rsync_sync.md).
func (s *sb) syncLocalDir(ctx context.Context, ld sandbox.LocalDir) (sandbox.SyncResult, error) {
	if err := s.rsyncCheck(); err != nil {
		return sandbox.SyncResult{}, err
	}
	src, err := resolveDir(ld.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("sandbox: LocalDir path %q: %w", ld.Path, err)
		}
		return sandbox.SyncResult{}, err
	}
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: LocalDir path %q is not a directory", ld.Path)
	}
	rs, err := rsyncx.Compute(ctx, src, rsyncx.RuleOptions{Ignore: ld.Ignore, IncludeIgnored: ld.IncludeIgnored, ExcludeGit: ld.ExcludeGit})
	if err != nil {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: LocalDir %q: %w", ld.Path, err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: LocalDir path %q: %w", ld.Path, err)
	}
	defer root.Close()
	start := &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC,
		IncludeIgnored: ld.IncludeIgnored, ExcludeGit: ld.ExcludeGit, Delete: ld.Delete}
	prepare := func(plan *sandboxpb.RsyncPlan) (*sandboxpb.RsyncArgs, transferFunc, error) {
		rules := rsyncx.Combine(rs.List(true), plan.Rules)
		if err := rsyncx.CheckRuleSize(rules); err != nil {
			return nil, nil, fmt.Errorf("sandbox: LocalDir %q: %w", ld.Path, err)
		}
		fsys := rsyncx.Filtered(root, ".", rsyncx.NewMatcher(rules))
		args := &sandboxpb.RsyncArgs{Rules: rules}
		var err error
		if args.Args, err = rsyncx.ServerArgs(rsyncx.ClientArgs(ld.Delete), false, "."); err != nil {
			return nil, nil, err
		}
		if ld.Delete {
			if args.Present, err = rsyncx.PresentSet(fsys, "."); err != nil {
				return nil, nil, fmt.Errorf("sandbox: LocalDir %q: %w", ld.Path, err)
			}
			if err := rsyncx.CheckRuleSize(args.Present); err != nil {
				return nil, nil, fmt.Errorf("sandbox: LocalDir %q: transfer set: %w", ld.Path, err)
			}
		}
		hostArgs, err := rsyncx.ServerArgs(rsyncx.ClientArgs(false), true, ".")
		if err != nil {
			return nil, nil, err
		}
		srv, err := rsyncx.NewServer(fsys)
		if err != nil {
			return nil, nil, err
		}
		return args, func(ctx context.Context, conn *rsyncx.Conn) error { return srv.Serve(ctx, conn, hostArgs) }, nil
	}
	done, sent, err := s.runRsync(ctx, start, prepare)
	if err != nil {
		return sandbox.SyncResult{}, err
	}
	return sandbox.SyncResult{Files: int(done.Files), Bytes: done.Bytes, Deleted: int(done.Deleted), Sent: sent}, nil
}

// exportLocalDir writes a workspace path into a host directory over the
// Rsync stream.
func (s *sb) exportLocalDir(ctx context.Context, path string, ld sandbox.LocalDir) error {
	if err := s.rsyncCheck(); err != nil {
		return err
	}
	return fmt.Errorf("%w: LocalDir export", sandbox.ErrUnsupported)
}

// transferFunc runs the host's side of the library over the stream.
type transferFunc func(context.Context, *rsyncx.Conn) error

// runRsync opens one Rsync stream, sends start, reads exactly one plan, has
// prepare build the args frame and the host's side of the transfer, runs it,
// and reads the done frame and status OK. It never retries. It returns the
// done frame and the rsync data bytes the host wrote.
func (s *sb) runRsync(ctx context.Context, start *sandboxpb.RsyncStart,
	prepare func(*sandboxpb.RsyncPlan) (*sandboxpb.RsyncArgs, transferFunc, error)) (*sandboxpb.RsyncDone, int64, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := s.client.Rsync(s.outgoing(streamCtx))
	if err != nil {
		return nil, 0, s.rsyncFailure(ctx, err)
	}
	if err := st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: start}}); err != nil {
		_, rerr := st.Recv()
		return nil, 0, s.rsyncFailure(ctx, rerr)
	}
	f, err := st.Recv()
	if err == io.EOF {
		return nil, 0, s.malformed("the stream ended before a plan frame")
	}
	if err != nil {
		return nil, 0, s.rsyncFailure(ctx, err)
	}
	plan := f.GetPlan()
	if plan == nil {
		return nil, 0, s.malformed(fmt.Sprintf("expected a plan frame, got a %s frame", rsyncx.FrameKind(f)))
	}
	args, run, err := prepare(plan)
	if err != nil {
		return nil, 0, err // our own refusal: the deferred cancel ends the stream, the sandbox stays live
	}
	if err := st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Args{Args: args}}); err != nil {
		_, rerr := st.Recv()
		return nil, 0, s.rsyncFailure(ctx, rerr)
	}
	conn := rsyncx.HostConn(st)
	runErr := run(streamCtx, conn)
	if runErr == nil {
		// Read up to the done frame, then the final status.
		_, runErr = io.Copy(io.Discard, conn)
	}
	_ = conn.Close()
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("sandbox: rsync: %w", err)
	}
	if fail := conn.Failure(); fail != nil {
		var fe *rsyncx.FrameError
		if errors.As(fail, &fe) {
			return nil, 0, s.malformed(fe.Error())
		}
		return nil, 0, s.rsyncFailure(ctx, fail)
	}
	if runErr != nil {
		// The library failed with the stream intact: cancel the stream (the
		// deferred cancel) and return the library's error; the sandbox stays
		// live.
		return nil, 0, fmt.Errorf("sandbox: rsync client: %w", runErr)
	}
	done, ok := conn.Done()
	if !ok {
		return nil, 0, s.malformed("the stream ended without a done frame")
	}
	if _, err := st.Recv(); err != io.EOF {
		if err == nil {
			return nil, 0, s.malformed("a frame after done")
		}
		return nil, 0, s.rsyncFailure(ctx, err)
	}
	return done, conn.Sent(), nil
}

// rsyncFailure classifies an Rsync stream error: a done caller context is
// the caller's; a deliberate refusal keeps the sandbox live and its gRPC
// status in the error chain; anything else is a transport failure.
func (s *sb) rsyncFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("sandbox: rsync: %w", ctx.Err())
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound:
		return fmt.Errorf("sandbox: agent refused Rsync: %w", err)
	case codes.Unimplemented:
		return fmt.Errorf("sandbox: agent protocol %d.%d does not implement Rsync: %w", s.agent.ProtocolMajor, s.agent.ProtocolMinor, err)
	}
	return s.classify(ctx, "Rsync", err)
}

// malformed makes the sandbox unavailable over a malformed Rsync response.
func (s *sb) malformed(reason string) error {
	cause := "malformed response: rsync: " + reason
	s.fail(cause)
	return fmt.Errorf("%w: %s", sandbox.ErrUnavailable, cause)
}
