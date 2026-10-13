package podman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

const (
	// procWait is how long the destroy step waits for the engine process to
	// exit on its own before killing its group.
	procWait = 5 * time.Second
	// killWait is how long it then waits for the killed process.
	killWait = 2 * time.Second
	// volumeRetry bounds the retries of a volume the engine reports in use.
	volumeRetry      = 10 * time.Second
	volumeRetryEvery = 500 * time.Millisecond
)

// destroy is the sandbox's destroy step, which attach runs on Close, at the
// TTL and on a failure inside Dial: wait for the engine process to exit (the
// connection is already closed, so the agent sees EOF and exits 0) and kill
// its group if it has not within procWait; remove the sandbox's container
// and its init container; remove the volume. Every action runs even when an
// earlier one failed, and the errors are joined. A container or volume that
// does not exist is not an error. proc is nil when the engine process never
// started.
func (p *provider) destroy(id string, proc process) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		if proc != nil {
			errs = append(errs, stopProc(proc))
		}
		errs = append(errs, p.remove(ctx, "rm", "-f", name(id)), p.remove(ctx, "rm", "-f", initName(id)), p.removeVolume(ctx, id))
		return errors.Join(errs...)
	}
}

// stopProc waits up to procWait for proc to exit, then kills its group and
// waits up to killWait more.
func stopProc(proc process) error {
	select {
	case <-proc.Done():
		return nil
	case <-time.After(procWait):
	}
	err := proc.KillGroup()
	select {
	case <-proc.Done():
	case <-time.After(killWait):
	}
	if err != nil {
		return fmt.Errorf("podman: killing the engine process: %w", err)
	}
	return nil
}

// remove runs a removal command; a missing container or volume is success.
func (p *provider) remove(ctx context.Context, args ...string) error {
	res, err := p.r.run(ctx, append([]string{p.bin}, args...), shortTimeout)
	if err == nil && missing(res.Output) {
		return nil
	}
	return p.cmdErr(args, res, err)
}

// removeVolume removes the sandbox's volume, retrying for up to volumeRetry
// while the engine reports it in use (a container that has just exited may
// still hold it).
func (p *provider) removeVolume(ctx context.Context, id string) error {
	args := []string{"volume", "rm", name(id)}
	deadline := time.Now().Add(volumeRetry)
	for {
		res, err := p.r.run(ctx, append([]string{p.bin}, args...), shortTimeout)
		if err != nil {
			return p.cmdErr(args, res, err)
		}
		if res.Outcome == tools.OutcomeOK || missing(res.Output) {
			return nil
		}
		if !inUse(res.Output) {
			return p.cmdErr(args, res, nil)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("podman: volume %s is still in use after %v: %s", name(id), volumeRetry, strings.TrimSpace(res.Output))
		}
		select {
		case <-ctx.Done():
		case <-time.After(volumeRetryEvery):
		}
	}
}

// missing reports whether out says the container or volume does not exist.
func missing(out string) bool { return strings.Contains(strings.ToLower(out), "no such") }

// inUse reports whether out says the volume is in use.
func inUse(out string) bool {
	o := strings.ToLower(out)
	return strings.Contains(o, "being used") || strings.Contains(o, "in use")
}
