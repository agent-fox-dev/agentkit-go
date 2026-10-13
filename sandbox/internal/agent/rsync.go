package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Rsync serves one LocalDir transfer. Frames come in the order start, plan,
// args, data both ways, done; one stream runs at a time. On SYNC the agent
// receives into the workspace (the host serves its directory), deletes what
// the host's transfer set lacks when asked, reports what changed and
// rebuilds the tool set. On EXPORT it serves a read-only, filtered view of
// the export path. A frame out of order or a bad start or args frame is
// InvalidArgument; a library failure while the stream is intact is
// FailedPrecondition.
func (a *Agent) Rsync(stream sandboxpb.Agent_RsyncServer) error {
	if !rsyncx.Supported {
		return status.Errorf(codes.FailedPrecondition, "LocalDir transfers are not supported on %s", runtime.GOOS)
	}
	if !a.rsync.acquire(stream.Context()) {
		return status.Error(codes.FailedPrecondition, "another Rsync stream is running")
	}
	defer a.rsync.release()
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return status.Errorf(codes.InvalidArgument, "expected a start frame, got a %s frame", rsyncx.FrameKind(first))
	}
	t, err := a.checkStart(start)
	if err != nil {
		return err
	}
	rs, err := rsyncx.Compute(ctx, t.root, rsyncx.RuleOptions{Ignore: tools.NoGlobalExcludes(),
		IncludeIgnored: start.IncludeIgnored, ExcludeGit: start.ExcludeGit || t.dir == rsyncx.DirExport, File: t.file, Base: a.root})
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "computing the rules: %v", err)
	}
	rules := rs.List(true)
	if err := rsyncx.CheckRuleSize(rules); err != nil {
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	if err := stream.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Plan{Plan: &sandboxpb.RsyncPlan{Rules: rules, File: t.file}}}); err != nil {
		return err
	}

	f, err := stream.Recv()
	if err != nil {
		return err
	}
	args := f.GetArgs()
	if args == nil {
		return status.Errorf(codes.InvalidArgument, "expected an args frame, got a %s frame", rsyncx.FrameKind(f))
	}
	if err := rsyncx.CheckServerArgs(args.Args, t.dir, start.Path); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	parsed, _ := rsyncx.ParseArgs(args.Args)
	for _, set := range [][]string{args.Rules, args.Present} {
		if err := rsyncx.CheckRuleSize(set); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	conn := rsyncx.AgentConn(stream)
	m := rsyncx.NewMatcher(args.Rules)
	// One module over the workspace root, writable only for SYNC: an EXPORT
	// stream only ever reads it, through a read-only view.
	if rsyncx.ModulesFor(a.root, t.dir)[0].Writable {
		return a.receive(ctx, stream, conn, m, parsed.Delete, args.Present)
	}
	return a.send(ctx, stream, conn, t, m, parsed.Delete, args.Args)
}

// rsyncSlot is the one Rsync stream slot. A stream that finds it held by a
// live stream is refused at once; one that finds it held by a stream whose
// context is already done (the host cancelled it, and the server is only
// unwinding) waits for the release, so a Sync right after a cancelled one is
// not refused.
type rsyncSlot struct {
	mu    sync.Mutex
	held  <-chan struct{} // the holder's context's Done; nil when free
	freed chan struct{}   // closed when the holder releases
}

// rsyncUnwind bounds the wait for a cancelled holder.
const rsyncUnwind = 5 * time.Second

func (s *rsyncSlot) acquire(ctx context.Context) bool {
	for {
		s.mu.Lock()
		if s.held == nil {
			s.held, s.freed = ctx.Done(), make(chan struct{})
			s.mu.Unlock()
			return true
		}
		held, freed := s.held, s.freed
		s.mu.Unlock()
		select {
		case <-held:
		default:
			return false
		}
		select {
		case <-freed:
		case <-ctx.Done():
			return false
		case <-time.After(rsyncUnwind):
			return false
		}
	}
}

func (s *rsyncSlot) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held = nil
	close(s.freed)
}

// transfer is a validated start frame.
type transfer struct {
	dir  rsyncx.Direction
	root string // the transfer root on disk
	rel  string // the export path, workspace-relative
	file bool   // the export path is a regular file
}

// checkStart validates a start frame.
func (a *Agent) checkStart(s *sandboxpb.RsyncStart) (transfer, error) {
	switch s.Direction {
	case sandboxpb.RsyncStart_SYNC:
		if s.Path != "" {
			return transfer{}, status.Errorf(codes.InvalidArgument, "start: path must be empty for SYNC, got %q", s.Path)
		}
		return transfer{dir: rsyncx.DirSync, root: a.root}, nil
	case sandboxpb.RsyncStart_EXPORT:
	default:
		return transfer{}, status.Errorf(codes.InvalidArgument, "start: direction %v is not SYNC or EXPORT", s.Direction)
	}
	rel, err := tarsafe.CleanRel(s.Path)
	if err != nil {
		return transfer{}, status.Errorf(codes.InvalidArgument, "start: path: %v", err)
	}
	ws, err := os.OpenRoot(a.root)
	if err != nil {
		return transfer{}, status.Errorf(codes.FailedPrecondition, "workspace: %v", err)
	}
	defer ws.Close()
	fi, err := ws.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return transfer{}, status.Errorf(codes.NotFound, "start: export path %q does not exist", s.Path)
	} else if err != nil {
		return transfer{}, status.Errorf(codes.InvalidArgument, "start: path %q: %v", s.Path, err)
	}
	t := transfer{dir: rsyncx.DirExport, root: filepath.Join(a.root, filepath.FromSlash(rel)), rel: rel}
	switch {
	case fi.Mode().IsRegular():
		t.file = true
		if s.Delete {
			return transfer{}, status.Errorf(codes.InvalidArgument, "start: delete with a regular-file path %q", s.Path)
		}
	case fi.IsDir():
	default:
		return transfer{}, status.Errorf(codes.InvalidArgument, "start: path %q is a symlink or not a regular file or directory", s.Path)
	}
	return t, nil
}

// receive runs a SYNC: the library's client writes the host's files into the
// workspace, AgentKit deletes what the host lacks when asked, and done
// reports what changed.
func (a *Agent) receive(ctx context.Context, stream sandboxpb.Agent_RsyncServer, conn *rsyncx.Conn, m *rsyncx.Matcher, del bool, present []string) error {
	before := snapshot(a.root)
	if err := rsyncx.RunClient(ctx, conn, rsyncx.ClientArgs(false), a.root); err != nil {
		return rsyncStatus(ctx, err)
	}
	if del {
		if _, err := rsyncx.DeleteAbsent(a.root, m, present); err != nil {
			return status.Errorf(codes.FailedPrecondition, "rsync: deleting: %v", err)
		}
	}
	files, bytes, deleted := diff(before, snapshot(a.root))
	if err := a.rebuild(); err != nil {
		return status.Errorf(codes.FailedPrecondition, "the files were synced, but rebuilding the tool set failed: %v", err)
	}
	return stream.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{
		Files: int64(files), Bytes: bytes, Deleted: int64(deleted)}}})
}

// send runs an EXPORT: the library's server sends a read-only view of the
// export path in which every excluded path does not exist, and done carries
// the transfer set when the host deletes.
func (a *Agent) send(ctx context.Context, stream sandboxpb.Agent_RsyncServer, conn *rsyncx.Conn, t transfer, m *rsyncx.Matcher, del bool, args []string) error {
	ws, err := os.OpenRoot(a.root)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "workspace: %v", err)
	}
	defer ws.Close()
	under := t.rel
	if t.file {
		under = path.Dir(t.rel)
	}
	fsys := rsyncx.Filtered(ws, under, m)
	srv, err := rsyncx.NewServer(fsys)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "rsync: %v", err)
	}
	if err := srv.Serve(ctx, conn, args); err != nil {
		return rsyncStatus(ctx, err)
	}
	done := &sandboxpb.RsyncDone{}
	if del {
		if done.Present, err = rsyncx.PresentSet(fsys, under); err != nil {
			return status.Errorf(codes.FailedPrecondition, "rsync: listing the transfer set: %v", err)
		}
		if err := rsyncx.CheckRuleSize(done.Present); err != nil {
			return status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	return stream.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: done}})
}

// rsyncStatus maps a library outcome: a done context (the host cancelled,
// or the TTL stopped the server) is its own status, a frame out of order is
// InvalidArgument, anything else FailedPrecondition with the library's
// message.
func rsyncStatus(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	var fe *rsyncx.FrameError
	if errors.As(err, &fe) {
		return status.Error(codes.InvalidArgument, fe.Error())
	}
	return status.Errorf(codes.FailedPrecondition, "rsync: %v", err)
}

// fileState is what the statistics compare.
type fileState struct {
	size    int64
	mtime   time.Time
	regular bool
}

// snapshot records every entry under root, without following symlinks.
func snapshot(root string) map[string]fileState {
	out := map[string]fileState{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out[p] = fileState{size: fi.Size(), mtime: fi.ModTime(), regular: fi.Mode().IsRegular()}
		return nil
	})
	return out
}

// diff counts the regular files that are new or changed in size or
// modification time, their bytes, and the entries that went away.
func diff(before, after map[string]fileState) (files int, bytes int64, deleted int) {
	for p, s := range after {
		if !s.regular {
			continue
		}
		if b, ok := before[p]; !ok || b.size != s.size || !b.mtime.Equal(s.mtime) || !b.regular {
			files++
			bytes += s.size
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			deleted++
		}
	}
	return files, bytes, deleted
}
