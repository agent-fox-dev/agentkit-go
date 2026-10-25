package landlock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

// initToolOptions are the init agent's tool options: its run_command output
// never spills.
var initToolOptions = sandbox.ToolOptions{DisableSpill: true}

// gitOutputTail bounds the output a failed clone carries.
const gitOutputTail = 2 << 10

var errSlot = errors.New("landlock: GitRepo must be the first Sync and come before Tools")

// defaultInitCommand is git with the clone arguments.
func defaultInitCommand(repo sandbox.GitRepo, ws string) []string {
	return append([]string{"git"}, gitclone.Args(repo, ws)...)
}

// syncGitRepo clones repo into the workspace through a second, confined
// agent with TCP allowed, holding the write lock throughout. Before starting
// anything it checks, in order, that the sandbox is live, that
// gitclone.Check accepts repo, that the slot is free and that the agent has
// git. A failure after the init agent started empties the workspace and
// leaves the slot free.
func (s *landlockSandbox) syncGitRepo(ctx context.Context, repo sandbox.GitRepo) (sandbox.SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := attach.Live(s.inner); err != nil {
		return sandbox.SyncResult{}, err
	}
	if err := gitclone.Check(repo); err != nil {
		return sandbox.SyncResult{}, err
	}
	if s.slotTaken() {
		return sandbox.SyncResult{}, errSlot
	}
	if !s.inner.Agent().Git {
		return sandbox.SyncResult{}, fmt.Errorf("landlock: git is not on the sandbox's PATH (%s); a GitRepo needs it", s.agentPATH)
	}
	expiry := s.created.Add(s.ttl)
	left := time.Until(expiry)
	if left <= 0 {
		return sandbox.SyncResult{}, fmt.Errorf("%w: the sandbox expired at %s", sandbox.ErrUnavailable, expiry.UTC().Format(time.RFC3339))
	}
	ttl := (left + time.Second - 1).Truncate(time.Second)
	started, err := s.runInit(ctx, repo, ttl, expiry)
	if err != nil {
		if started {
			err = errors.Join(err, emptyDir(s.ws))
		}
		return sandbox.SyncResult{}, err
	}
	s.take()
	return sandbox.SyncResult{}, nil
}

// expired reports whether the clone ran into the sandbox's expiry: ictx's
// deadline is the expiry, but ictx reports it only once its timer has fired,
// and the init agent, whose TTL is the expiry rounded up to a second, can end
// the clone's connection a moment before that. The clock decides as well.
func expired(ictx context.Context, expiry time.Time) bool {
	return ictx.Err() != nil || !time.Now().Before(expiry)
}

// runInit starts the init agent, checks its report, runs the clone through
// its run_command tool and always closes it. started reports whether the
// init agent was started.
func (s *landlockSandbox) runInit(ctx context.Context, repo sandbox.GitRepo, ttl time.Duration, expiry time.Time) (started bool, err error) {
	p := s.p
	spec := sandbox.Spec{TTL: ttl, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, Workdir: s.ws}
	args := agentArgs(spec, p.extraRO, p.extraRW, p.seams.resolver("/etc/resolv.conf"))
	h, conn, err := p.seams.start(startReq{bin: p.bin, args: args, env: initEnv(s.dir, s.agentPATH), dir: s.ws})
	if err != nil {
		return false, fmt.Errorf("landlock: starting the GitRepo init agent: %w", err)
	}
	destroy := func(ctx context.Context) error {
		return errors.Join(stopAgent(h, stopWait), sessionKill(ctx, p.seams.procs, h.pid, sessionWait))
	}
	init, err := attach.Dial(ctx, conn, attach.Config{ID: s.inner.ID() + "-init", Spec: sandbox.Spec{Network: spec.Network, TTL: ttl}, Destroy: destroy, Provider: "landlock"})
	if err != nil {
		return true, fmt.Errorf("landlock: attaching to the GitRepo init agent: %w", err)
	}
	defer func() { err = errors.Join(err, init.Close(context.Background())) }()
	l := init.Agent().Landlock
	wsReal, rerr := filepath.EvalSymlinks(s.ws)
	if l.ABI < sandbox.LandlockTCPABI || rerr != nil || !slices.Contains(l.Writable, wsReal) {
		return true, fmt.Errorf("%w: the GitRepo init agent reported Landlock ABI %d (kernel ABI %d), TCP %t, note %q, writable %v",
			ErrNoLandlock, l.ABI, l.KernelABI, l.TCP, l.Note, l.Writable)
	}
	ts, err := init.Tools(ctx, initToolOptions)
	if err != nil {
		return true, fmt.Errorf("landlock: the GitRepo init agent's tools: %w", err)
	}
	ictx, cancel := context.WithDeadline(ctx, expiry)
	defer cancel()
	res := runCommand(ictx, ts, p.seams.initCommand(repo, s.ws))
	switch {
	case ctx.Err() != nil:
		return true, fmt.Errorf("landlock: GitRepo clone of %s: %w", repo.URL, ctx.Err())
	case expired(ictx, expiry):
		return true, fmt.Errorf("%w: the sandbox expired during the GitRepo clone of %s", sandbox.ErrUnavailable, repo.URL)
	case res.OK:
		return true, nil
	}
	out := tailOf(resultOutput(res), gitOutputTail)
	if code := exitCode(res); res.Error == "exec_failed" || code == 126 || code == 127 {
		return true, fmt.Errorf("landlock: git could not be run under the sandbox's ruleset; install git beneath a system directory or add its directory to Options.ExtraRO: %s", out)
	}
	return true, fmt.Errorf("landlock: GitRepo clone of %s failed (%s); output: %s", repo.URL, res.Error, out)
}

// initEnv is the init agent's whole environment: no credential prompt, no
// system git configuration (credential helpers, URL rewrites), the
// sandbox's HOME and TMPDIR, and the PATH its agent got. No Spec.Env value.
func initEnv(dir, agentPATH string) []string {
	return render(map[string]string{"GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
		"HOME": filepath.Join(dir, "home"), "TMPDIR": filepath.Join(dir, "tmp"), "PATH": agentPATH})
}

// runCommand calls the run_command proxy with argv.
func runCommand(ctx context.Context, ts []core.Tool, argv []string) core.ToolResult {
	input, _ := json.Marshal(map[string][]string{"argv": argv})
	for _, t := range ts {
		if t.Name == "run_command" {
			return t.Execute(ctx, input)
		}
	}
	return core.ToolResult{Error: "exec_failed", Detail: "the init agent has no run_command tool"}
}

// resultOutput is a run_command result's output.
func resultOutput(r core.ToolResult) string {
	if o, ok := r.Data["output"].(string); ok && o != "" {
		return o
	}
	return r.Text + r.Detail
}

// exitCode is a run_command result's exit code, or -1.
func exitCode(r core.ToolResult) int {
	switch v := r.Data["exit_code"].(type) {
	case json.Number: // the proxy decodes Data with UseNumber
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case float64:
		return int(v)
	case int:
		return v
	}
	return -1
}

// tailOf is the last n bytes of s, trimmed.
func tailOf(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
