package podman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// diagTimeout bounds the missing-agent diagnostic.
const diagTimeout = 30 * time.Second

// diagArgs is the missing-agent diagnostic: `run --rm` of the image with
// the reserved labels, the hardening flags, the runtime and userns flags,
// and `--entrypoint sandbox.AgentPath ... --version`. It has no --init, whose
// init process would decide the exit status, no -i and no volume, so 126 and
// 127 are the runtime's own "cannot execute" and "not found".
func diagArgs(o Options, id string, created time.Time, spec sandbox.Spec) []string {
	args := []string{"run", "--rm"}
	args = append(args, labelArgs(id, created, sandbox.Spec{TTL: spec.TTL})...)
	args = append(args, "--log-driver", "none", "--network", "none")
	args = append(args, hardeningArgs()...)
	args = append(args, runtimeArgs(o)...)
	return append(args, "--entrypoint", sandbox.AgentPath, spec.Image, "--version")
}

// createFailed classifies a failed attach.Dial, after attach has already
// run the destroy step: protocol and RequireLandlock refusals pass through,
// a done ctx is ctx's error, an engine error (exit 125) carries the stderr
// tail, and any other non-zero exit runs the diagnostic, which tells an
// image without an agent from an agent that failed.
func (p *provider) createFailed(ctx context.Context, err error, proc process, id string, created time.Time, eff sandbox.Spec) error {
	var se *sandbox.SpecError
	if errors.Is(err, sandbox.ErrProtocol) || errors.As(err, &se) && se.Field == "RequireLandlock" {
		return err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("podman: create: %w: %w", ctx.Err(), err)
	}
	select {
	case <-proc.Done():
	case <-time.After(procWait + killWait):
	}
	tail := proc.StderrTail()
	handshake := fmt.Errorf("podman: create: %w; engine stderr: %s", err, tail)
	select {
	case <-proc.Done():
	default:
		return handshake
	}
	switch code := proc.ExitCode(); code {
	case 0:
		return handshake
	case 125:
		return fmt.Errorf("podman: create: %s run failed with exit status 125 (an engine error); engine stderr: %s; %w", p.engine, tail, err)
	}
	dctx, cancel := context.WithTimeout(context.Background(), diagTimeout)
	defer cancel()
	res, derr := p.r.run(dctx, append([]string{p.bin}, diagArgs(p.opts, id, created, eff)...), diagTimeout)
	if derr != nil || (res.ExitCode != 126 && res.ExitCode != 127) {
		return handshake
	}
	return errors.Join(noAgentErr(eff.Image, strings.TrimSpace(res.Output)+"\n"+tail), err)
}

// noAgentErr is ErrNoAgent for image, with the Containerfile line that adds
// the agent from the overlay image of this build's version.
func noAgentErr(image, output string) error {
	ref, err := sandbox.OverlayImage()
	hint := ""
	if err != nil {
		ref = sandbox.OverlayImageRepo + ":<version>"
		hint = " (this build's version has no published overlay image: build one with make build-containers, or use a release's X.Y.Z tag)"
	}
	return fmt.Errorf("podman: %w: image %q cannot execute %s; add the agent to the image with this Containerfile line%s:\n\t%s\nengine output: %s",
		sandbox.ErrNoAgent, image, sandbox.AgentPath, hint, sandbox.OverlayCopyLine(ref), strings.TrimSpace(output))
}
