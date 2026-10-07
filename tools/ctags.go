package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// ErrCtagsUnavailable is returned by a CtagsRunner when ctags is not on PATH
// or is not Universal Ctags.
var ErrCtagsUnavailable = errors.New("tools: ctags unavailable")

// ctagsAccumulatorCap is the byte budget for the stdout accumulator.
// Tests may lower it to exercise the truncation path.
var ctagsAccumulatorCap = 16 * 1024 * 1024 // 16 MiB

// CtagsRunner returns a function that runs universal-ctags with the given
// arguments and returns its stdout. The returned function is assignable to
// outline.Options.Runner without tools importing outline.
//
// The runner locates ctags on its environment's PATH and confirms it is
// Universal Ctags by running `ctags --version`. If ctags is absent or not
// universal, calls return ErrCtagsUnavailable without spawning until
// ctagsRetryInterval (a minute) has passed, and then probe again.
//
// The process runs in its own process group and is killed when the context
// ends, exactly as execute does. Stdout is captured into an Accumulator in
// TruncateHead mode; if the accumulator reports truncation the call returns
// an error rather than a partial stream.
func CtagsRunner(env []string) func(ctx context.Context, args []string) ([]byte, error) {
	var (
		mu       sync.Mutex
		probed   bool
		probedAt time.Time
		ctagsBin string
		initErr  error
	)

	resolvedEnv := env
	if resolvedEnv == nil {
		resolvedEnv = ReducedEnv(nil)
	}

	return func(ctx context.Context, args []string) ([]byte, error) {
		// Locate and verify ctags once per runner. The probe is bounded by
		// ctagsProbeTimeout and by the caller's context: a wedged ctags is
		// killed at the deadline (spec 02 DD14) rather than holding the
		// symbol table's lock forever. A probe cut short by the CALLER is
		// not a verdict on ctags, so it is not remembered.
		//
		// A FOUND ctags is remembered for the runner's life. "Unavailable" is
		// remembered for ctagsRetryInterval only: it is a fact about the
		// machine at that moment, and ctags installed while the process runs
		// was otherwise never picked up.
		mu.Lock()
		if !probed || (initErr != nil && time.Since(probedAt) >= ctagsRetryInterval) {
			bin, err := locateUniversalCtags(ctx, resolvedEnv)
			if ctx.Err() != nil {
				mu.Unlock()
				return nil, ctx.Err()
			}
			ctagsBin, initErr, probed, probedAt = bin, err, true, time.Now()
		}
		bin, err := ctagsBin, initErr
		mu.Unlock()
		if err != nil {
			return nil, err
		}

		return runCtagsProcess(ctx, bin, resolvedEnv, args)
	}
}

// ctagsRetryInterval is how long an "unavailable" probe result stands before
// the next call probes again. A variable so a test can shorten it.
var ctagsRetryInterval = time.Minute

// ctagsProbeTimeout bounds `ctags --version`. A variable so a test can
// shorten it.
var ctagsProbeTimeout = 5 * time.Second

// locateUniversalCtags finds ctags on the PATH of env — the environment it
// will run with — and verifies it is Universal Ctags.
// The version probe runs like any ctags call — its own process group, killed
// when ctx or ctagsProbeTimeout ends.
func locateUniversalCtags(ctx context.Context, env []string) (string, error) {
	bin, err := lookPathFor("ctags", env)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCtagsUnavailable, err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, ctagsProbeTimeout)
	defer cancel()
	out, err := runCtagsProcess(probeCtx, bin, env, []string{"--version"})
	if err != nil {
		return "", fmt.Errorf("%w: ctags --version failed: %v", ErrCtagsUnavailable, err)
	}
	if !bytes.Contains(out, []byte("Universal Ctags")) {
		return "", fmt.Errorf("%w: ctags is not Universal Ctags", ErrCtagsUnavailable)
	}

	return bin, nil
}

// runCtagsProcess executes ctags with the given arguments and returns its stdout.
// It follows the same process-group lifecycle as runArgv in exec.go:
// exec.Command (not CommandContext), setProcessGroup, killGroup on ctx.Done.
func runCtagsProcess(ctx context.Context, bin string, env []string, args []string) ([]byte, error) {
	// Check context before starting.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stderr = nil // discard stderr

	acc := NewAccumulator(ctagsAccumulatorCap, TruncateHead)
	cmd.Stdout = acc

	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ctags start: %w", err)
	}

	// Kill the process group on context cancellation, as runArgv does.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killGroup(cmd)
		case <-done:
		}
	}()

	waitErr := cmd.Wait()
	close(done)

	// Check context first: a cancelled context is the priority error.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}

	// Check for truncation: a partial stream is not usable.
	if acc.Truncated() {
		return nil, fmt.Errorf("ctags output truncated (%d bytes total, cap %d)",
			acc.Total(), ctagsAccumulatorCap)
	}

	// Check exit status.
	if waitErr != nil {
		return nil, fmt.Errorf("ctags exited with error: %w", waitErr)
	}

	// Success: return the captured stdout.
	result := make([]byte, len(acc.head))
	copy(result, acc.head)
	return result, nil
}
