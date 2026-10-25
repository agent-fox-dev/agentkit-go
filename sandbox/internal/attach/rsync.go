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
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
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

// absPath refuses an empty LocalDir path and joins a relative one to the
// working directory at call time.
func absPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("sandbox: LocalDir path %q is empty", p)
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("sandbox: LocalDir path %q: %v", p, err)
	}
	return filepath.Join(wd, p), nil
}

// resolveDir resolves a LocalDir path once: a relative path joins the
// working directory at call time, and symlinks are evaluated.
func resolveDir(p string) (string, error) {
	abs, err := absPath(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// syncLocalDir fills the workspace from a host directory: the host serves a
// filtered view of the directory, the agent's client receives it, and the
// agent reports what changed. The sending side serves a filtered view because
// the rsync library cannot apply exclude rules safely itself (see
// docs/adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md).
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

// exportLocalDir writes a workspace path into a host directory: the agent
// serves a filtered, read-only view of the path, the host's client receives
// into the destination, and the host deletes what the agent's transfer set
// lacks when asked. The deletion is AgentKit's own because the library's
// --delete consults no rule and would remove excluded paths such as dst/.git
// (see docs/adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md).
func (s *sb) exportLocalDir(ctx context.Context, path string, ld sandbox.LocalDir) error {
	if err := s.rsyncCheck(); err != nil {
		return err
	}
	rel, err := tarsafe.CleanRel(path)
	if err != nil {
		return fmt.Errorf("sandbox: export path: %w", err)
	}
	dst, exists, err := resolveDest(ld.Path)
	if err != nil {
		return err
	}
	rs := rsyncx.RuleSet{Git: true}
	if exists {
		if rs, err = rsyncx.Compute(ctx, dst, rsyncx.RuleOptions{Ignore: ld.Ignore, IncludeIgnored: ld.IncludeIgnored, ExcludeGit: true}); err != nil {
			return fmt.Errorf("sandbox: LocalDir %q: %w", ld.Path, err)
		}
	}
	start := &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: rel,
		IncludeIgnored: ld.IncludeIgnored, ExcludeGit: ld.ExcludeGit, Delete: ld.Delete}
	var m *rsyncx.Matcher
	prepare := func(plan *sandboxpb.RsyncPlan) (*sandboxpb.RsyncArgs, transferFunc, error) {
		// A single file takes no ignore rules, on either side.
		rules := rsyncx.Combine(rs.List(!plan.File), plan.Rules)
		if err := rsyncx.CheckRuleSize(rules); err != nil {
			return nil, nil, fmt.Errorf("sandbox: LocalDir %q: %w", ld.Path, err)
		}
		m = rsyncx.NewMatcher(rules)
		operand := rel
		if !plan.File && rel != "." {
			operand = rel + "/"
		}
		args := &sandboxpb.RsyncArgs{Rules: rules}
		if args.Args, err = rsyncx.ServerArgs(rsyncx.ClientArgs(ld.Delete), true, operand); err != nil {
			return nil, nil, err
		}
		if !exists {
			if err := os.Mkdir(dst, 0o755); err != nil {
				return nil, nil, fmt.Errorf("sandbox: LocalDir path %q: %w", ld.Path, err)
			}
		}
		return args, func(ctx context.Context, conn *rsyncx.Conn) error {
			return rsyncx.RunClient(ctx, conn, rsyncx.ClientArgs(false), dst)
		}, nil
	}
	done, _, err := s.runRsync(ctx, start, prepare)
	if err != nil {
		return err
	}
	if ld.Delete {
		if _, err := rsyncx.DeleteAbsent(dst, m, done.Present); err != nil {
			return fmt.Errorf("sandbox: LocalDir %q: deleting: %w", ld.Path, err)
		}
	}
	return nil
}

// resolveDest resolves an Export destination once, creating nothing: an
// existing path must be a directory; a missing one needs an existing parent
// directory. It reports whether the destination exists.
func resolveDest(p string) (string, bool, error) {
	abs, err := absPath(p)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Lstat(abs); err == nil {
		dst, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", false, fmt.Errorf("sandbox: LocalDir path %q: %w", p, err)
		}
		if fi, err := os.Stat(dst); err != nil || !fi.IsDir() {
			return "", false, fmt.Errorf("sandbox: LocalDir path %q is not a directory", p)
		}
		return dst, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("sandbox: LocalDir path %q: %w", p, err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(parent); err == nil && !fi.IsDir() {
			err = errors.New("not a directory")
		}
	}
	if err != nil {
		return "", false, fmt.Errorf("sandbox: LocalDir path %q: its parent directory is unusable: %v", p, err)
	}
	return filepath.Join(parent, filepath.Base(abs)), false, nil
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
	if conn.Ended() {
		return nil, 0, s.malformed("the stream ended without a done frame")
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
