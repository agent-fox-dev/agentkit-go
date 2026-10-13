package landlock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
)

// Create checks spec, makes <Root>/<id>/ with its marker and private
// workspace/, home/ and tmp/, starts the agent confined to them in its own
// session, attaches, and checks the Landlock report against the ABI 4
// floor. Every failure leaves nothing behind.
func (p *provider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	eff, err := sandbox.CheckSpec(p.Capabilities(), spec)
	if err != nil {
		return nil, err
	}
	if err := checkSpec(eff); err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(p.root, id)
	created := time.Now()
	if err := p.makeDir(dir, eff, created); err != nil {
		return nil, errors.Join(err, removeTree(dir))
	}
	ws := filepath.Join(dir, "workspace")
	eff.Workdir = ws
	resolver := ""
	if eff.Network.Mode == sandbox.NetworkUnrestricted {
		resolver = p.seams.resolver("/etc/resolv.conf")
	}
	env := agentEnv(eff.Env, dir)
	h, conn, err := p.seams.start(startReq{bin: p.bin, args: agentArgs(eff, p.extraRO, p.extraRW, resolver), env: env, dir: ws})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("landlock: starting agentkit-sandboxd: %w", err), removeTree(dir))
	}
	destroy := newDestroy(h, p.seams.procs, dir, removeTree)
	if err := p.markAgent(dir, h.pid, eff, created); err != nil {
		_ = conn.Close()
		return nil, errors.Join(err, destroy(context.Background()))
	}
	inner, err := attach.Dial(ctx, conn, attach.Config{ID: id, Spec: eff, Destroy: destroy, Provider: "landlock"})
	if err != nil {
		return nil, err
	}
	if err := p.checkReport(inner.Agent().Landlock, dir, eff.Network.Mode); err != nil {
		return nil, errors.Join(err, inner.Close(context.Background()))
	}
	return &landlockSandbox{inner: inner, p: p, dir: dir, ws: ws, created: created, agentPATH: envValue(env, "PATH")}, nil
}

// checkSpec refuses what CheckSpec allows but this provider does not
// honour: any Spec.Provider key, and HOME or TMPDIR in Spec.Env, which the
// provider sets to the sandbox's private directories.
func checkSpec(eff sandbox.Spec) error {
	if len(eff.Provider) > 0 {
		k := slices.Sorted(maps.Keys(eff.Provider))[0]
		return &sandbox.SpecError{Provider: "landlock", Field: "Provider", Reason: fmt.Sprintf("it defines no Spec.Provider options, and got %q", k)}
	}
	for _, v := range []string{"HOME", "TMPDIR"} {
		if _, ok := eff.Env[v]; ok {
			return &sandbox.SpecError{Provider: "landlock", Field: "Env",
				Reason: v + " is set by the landlock provider to the sandbox's private directory; it feeds the Landlock ruleset"}
		}
	}
	return nil
}

// newID is 16 random lowercase hex characters.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// makeDir creates dir (0700), writes the creator marker at once, then
// creates workspace/, home/ and tmp/, each 0700 whatever the umask.
func (p *provider) makeDir(dir string, eff sandbox.Spec, created time.Time) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	m, err := p.ownerMarker(os.Getpid(), "creator", eff, created)
	if err != nil {
		return err
	}
	if err := writeMarker(dir, m); err != nil {
		return err
	}
	for _, sub := range []string{"workspace", "home", "tmp"} {
		d := filepath.Join(dir, sub)
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// markAgent replaces the marker so that it names the agent.
func (p *provider) markAgent(dir string, pid int, eff sandbox.Spec, created time.Time) error {
	m, err := p.ownerMarker(pid, "agent", eff, created)
	if err != nil {
		return err
	}
	return replaceMarker(dir, m)
}

// ownerMarker is the marker naming pid in role.
func (p *provider) ownerMarker(pid int, role string, eff sandbox.Spec, created time.Time) (marker, error) {
	start, ok, err := p.seams.procs.lookup(pid)
	if err == nil && !ok {
		err = fmt.Errorf("process %d is gone", pid)
	}
	if err != nil {
		return marker{}, fmt.Errorf("landlock: reading the start time of %d: %w", pid, err)
	}
	boot, err := p.seams.procs.bootID()
	if err != nil {
		return marker{}, fmt.Errorf("landlock: reading the boot id: %w", err)
	}
	return marker{PID: pid, StartTime: start, BootID: boot, Role: role, Created: created, TTL: eff.TTL,
		Network: eff.Network.Mode.String(), Labels: eff.Labels}, nil
}

// checkReport holds the agent's Landlock report to the provider's floor,
// whatever Spec.RequireLandlock says.
func (p *provider) checkReport(l sandbox.LandlockInfo, dir string, mode sandbox.NetworkMode) error {
	ok := l.ABI >= sandbox.LandlockTCPABI && (mode != sandbox.NetworkNone || l.TCP)
	for _, sub := range []string{"workspace", "home", "tmp"} {
		r, err := filepath.EvalSymlinks(filepath.Join(dir, sub))
		if err != nil || !slices.Contains(l.Writable, r) {
			ok = false
		}
	}
	if !ok {
		return errors.New("landlock: the agent's Landlock report falls short")
	}
	for _, root := range append(slices.Clone(l.Writable), l.ReadOnly...) {
		if within(root, p.root) && !within(dir, root) {
			return errors.New("landlock: a ruleset root contains Options.Root")
		}
	}
	return nil
}
