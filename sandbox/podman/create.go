package podman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// destroyBudget is attach's destroy budget, used where the provider runs
// the destroy step itself.
const destroyBudget = 30 * time.Second

// Create checks spec, creates the sandbox's labelled volume, starts the
// engine process with Args and attaches to the agent over its stdio. When
// Spec.Image is empty the image is sandbox.DefaultImage(). Whatever fails
// after the volume exists, the container and the volume are removed before
// Create returns.
func (p *provider) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	eff, err := sandbox.CheckSpec(p.Capabilities(), spec)
	if err != nil {
		return nil, err
	}
	if eff.Image == "" {
		if eff.Image, err = p.defaultImage(); err != nil {
			return nil, err
		}
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	created := p.now()
	vc := append(append([]string{"volume", "create"}, labelArgs(id, created, eff)...), name(id))
	if err := p.short(ctx, vc...); err != nil {
		return nil, err
	}
	proc, err := p.r.start(append([]string{p.bin}, Args(p.opts, id, created, eff)...), p.env)
	if err != nil {
		dctx, cancel := context.WithTimeout(context.Background(), destroyBudget)
		defer cancel()
		return nil, errors.Join(fmt.Errorf("podman: starting %s run: %w", p.engine, err), p.destroy(id, nil)(dctx))
	}
	inner, err := attach.Dial(ctx, stdioconn.New(proc.Stdout(), proc.Stdin()),
		attach.Config{ID: id, Spec: eff, Destroy: p.destroy(id, proc), Provider: string(p.engine)})
	if err != nil {
		return nil, p.createFailed(ctx, err, proc, id, created, eff)
	}
	return &podSandbox{inner: inner, p: p, id: id, created: created, eff: eff}, nil
}

// newID is 16 random lowercase hex characters.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// short runs the short engine command args and turns a failure into an
// error that names the command and carries its output tail.
func (p *provider) short(ctx context.Context, args ...string) error {
	res, err := p.r.run(ctx, append([]string{p.bin}, args...), shortTimeout)
	return p.cmdErr(args, res, err)
}

// cmdErr is nil for a command that ran and exited 0, and otherwise an error
// naming the engine, the command and how it ended.
func (p *provider) cmdErr(args []string, res tools.ExecResult, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("podman: %s %s: %w", p.engine, strings.Join(args, " "), err)
	case res.Outcome == tools.OutcomeOK:
		return nil
	case res.Outcome == tools.OutcomeExit:
		return fmt.Errorf("podman: %s %s: exit status %d: %s", p.engine, strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Output))
	}
	return fmt.Errorf("podman: %s %s: %s: %s", p.engine, strings.Join(args, " "), res.Outcome, strings.TrimSpace(res.Output))
}
