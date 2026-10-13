package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agentenv"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// toolOptions are a Tools request's options, comparable.
type toolOptions struct {
	maxFiles, maxDuration int64
	spillDir              string
	disableSpill          bool
}

// toolSet is a configured tool set and the answer it was configured with.
type toolSet struct {
	opts   toolOptions
	byName map[string]core.Tool
	names  []string
	resp   *sandboxpb.ToolsResponse
}

// Tools configures the tool set: tools.All over the workspace with the
// explicit environment and no global git excludes. A repeat with the same
// options returns the same answer; one with different options fails with
// FailedPrecondition and leaves the set unchanged.
func (a *Agent) Tools(_ context.Context, req *sandboxpb.ToolsRequest) (*sandboxpb.ToolsResponse, error) {
	opts := toolOptions{req.SymbolsMaxFiles, req.SymbolsMaxDurationNanos, req.SpillDir, req.DisableSpill}
	if opts.spillDir != "" && !filepath.IsAbs(opts.spillDir) {
		return nil, status.Errorf(codes.InvalidArgument, "spill_dir %q is not an absolute path", opts.spillDir)
	}
	// Under a ruleset a spill directory outside the writable roots would
	// fail on the first result that spills; refuse it once, here. The
	// default spill directory is never checked.
	if a.landlock.ABI > 0 && opts.spillDir != "" {
		roots := writableDirs(a.landlock.Writable)
		if !beneathAny(filepath.Clean(opts.spillDir), roots) {
			return nil, status.Errorf(codes.InvalidArgument, "spill_dir %q is not beneath a writable Landlock root %q", opts.spillDir, roots)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.set != nil {
		if a.set.opts != opts {
			return nil, status.Error(codes.FailedPrecondition, "the tool set is already configured with different options")
		}
		return a.set.resp, nil
	}
	set, err := a.buildSet(opts)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "building the tool set: %v", err)
	}
	a.set = set
	return set.resp, nil
}

// writableDirs are the directories among the ruleset's writable roots (the
// device files are not).
func writableDirs(writable []string) []string {
	var out []string
	for _, p := range writable {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// beneathAny reports whether path is one of roots or lies beneath one, by
// path components.
func beneathAny(path string, roots []string) bool {
	for _, r := range roots {
		if rel, err := filepath.Rel(r, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

// buildSet builds the tool set for opts.
func (a *Agent) buildSet(opts toolOptions) (*toolSet, error) {
	spill := opts.spillDir
	if spill == "" && !opts.disableSpill {
		dir, _ := agentenv.TempDir(a.env)
		spill = filepath.Join(dir, "agentkit-spill")
	}
	ws, err := tools.NewWorkspace(a.root)
	if err != nil {
		return nil, err
	}
	all, err := a.build(tools.Options{
		Workspace:    ws,
		Env:          a.env,
		Ignore:       tools.NoGlobalExcludes(),
		Symbols:      tools.SymbolOptions{MaxFiles: int(opts.maxFiles), MaxDuration: time.Duration(opts.maxDuration)},
		SpillDir:     spill,
		DisableSpill: opts.disableSpill,
	})
	if err != nil {
		return nil, err
	}
	set := &toolSet{opts: opts, byName: map[string]core.Tool{}, resp: &sandboxpb.ToolsResponse{}}
	for _, t := range all {
		set.byName[t.Name] = t
		set.names = append(set.names, t.Name)
		set.resp.Tools = append(set.resp.Tools, &sandboxpb.ToolInfo{Name: t.Name, Digest: tooldigest.Digest(t)})
	}
	return set, nil
}

// rebuild remakes a configured tool set with its options, so the symbol
// table and the reference cache see files a sync wrote. Calls in flight keep
// the set they started on. On failure the previous set stays.
func (a *Agent) rebuild() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.set == nil {
		return nil
	}
	set, err := a.buildSet(a.set.opts)
	if err != nil {
		return err
	}
	set.resp = a.set.resp
	a.set = set
	return nil
}
