package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
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
// The runner locates ctags with exec.LookPath once and confirms it is
// Universal Ctags by running `ctags --version`. If ctags is absent or not
// universal, every call returns ErrCtagsUnavailable without spawning.
//
// The process runs in its own process group and is killed when the context
// ends, exactly as execute does. Stdout is captured into an Accumulator in
// TruncateHead mode; if the accumulator reports truncation the call returns
// an error rather than a partial stream.
func CtagsRunner(env []string) func(ctx context.Context, args []string) ([]byte, error) {
	var (
		once     sync.Once
		ctagsBin string
		initErr  error
	)

	resolvedEnv := env
	if resolvedEnv == nil {
		resolvedEnv = ReducedEnv(nil)
	}

	return func(ctx context.Context, args []string) ([]byte, error) {
		// Locate and verify ctags exactly once per runner.
		once.Do(func() {
			ctagsBin, initErr = locateUniversalCtags(resolvedEnv)
		})
		if initErr != nil {
			return nil, initErr
		}

		return runCtagsProcess(ctx, ctagsBin, resolvedEnv, args)
	}
}

// locateUniversalCtags finds ctags on PATH and verifies it is Universal Ctags.
func locateUniversalCtags(env []string) (string, error) {
	bin, err := exec.LookPath("ctags")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCtagsUnavailable, err)
	}

	// Run ctags --version and check for "Universal Ctags" in the output.
	cmd := exec.Command(bin, "--version")
	cmd.Env = env
	cmd.Stdin = nil
	out, err := cmd.Output()
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
