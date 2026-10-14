package landlock

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

// idRe matches a sandbox directory's name.
var idRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// lookupResult is what the process table says about a marker's pid: whether
// it exists, its start time, and the current boot id.
type lookupResult struct {
	present bool
	start   uint64
	boot    string
}

// ownerAlive reports whether a marker's owner is still the process it
// recorded: its pid exists with the recorded start time on the recorded
// boot. Signal 0 cannot tell a reused pid; the start time can.
func ownerAlive(m marker, present bool, start uint64, boot string) bool {
	return present && start == m.StartTime && boot == m.BootID
}

// decide is Reap's rule for one sandbox: keep it exactly when its owner is
// alive, whatever the role.
func decide(m marker, l lookupResult) bool { return ownerAlive(m, l.present, l.start, l.boot) }

// Reap removes the sandboxes under Root whose owner is gone and returns
// their ids, sorted. It considers only directories (never symlinks) named by
// 16 lowercase hex characters with a readable marker, and leaves everything
// else alone. A sandbox whose owner is alive is skipped, whichever process
// created it. For a dead agent on the same boot it first kills what is left
// of the agent's session. Failed removals do not stop the others; their
// errors are joined and returned with the ids that were removed.
func (p *provider) Reap(ctx context.Context) ([]string, error) {
	ents, err := os.ReadDir(p.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	boot, err := p.seams.procs.bootID()
	if err != nil {
		return nil, fmt.Errorf("landlock: reading the boot id: %w", err)
	}
	var ids []string
	var errs []error
	for _, e := range ents {
		dir := filepath.Join(p.root, e.Name())
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() || !idRe.MatchString(e.Name()) {
			continue
		}
		m, err := readMarker(dir)
		if err != nil {
			continue
		}
		start, present, err := p.seams.procs.lookup(m.PID)
		if err != nil {
			errs = append(errs, fmt.Errorf("landlock: reaping %s: %w", e.Name(), err))
			continue
		}
		if decide(m, lookupResult{present: present, start: start, boot: boot}) {
			continue
		}
		var kerr error
		if m.Role == "agent" && m.BootID == boot && !present {
			kerr = sessionKill(ctx, p.seams.procs, m.PID, sessionWait)
		}
		if err := errors.Join(kerr, p.seams.remove(dir)); err != nil {
			errs = append(errs, fmt.Errorf("landlock: reaping %s: %w", e.Name(), err))
			continue
		}
		ids = append(ids, e.Name())
	}
	slices.Sort(ids)
	return ids, errors.Join(errs...)
}
