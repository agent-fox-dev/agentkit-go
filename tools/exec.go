package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// fileWriter is the interface the Accumulator's spill/log file writer must
// satisfy. In production it is the *os.File itself; in tests it can be
// replaced through wrapExecFile to inject write failures.
type fileWriter interface {
	io.Writer
	io.Closer
}

// wrapExecFile wraps the *os.File used for the spill or log file. In
// production it returns the file unchanged. Tests replace it to inject write
// failures (TS-04-15).
var wrapExecFile = func(w *os.File) fileWriter { return w }

// waitCmd calls cmd.Wait. Tests replace it to inject non-ExitError Wait
// errors (TS-04-27).
var waitCmd = func(c *exec.Cmd) error { return c.Wait() }

// Outcome classifies how a command ended. It is a distinct type because the
// classification is a PURE FUNCTION with a pinned precedence, unit-tested on
// its own (REQ-TOOL-17.6): mixing it into the run path is how "the command
// timed out" ends up reported as "exit status 1".
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeExit    Outcome = "exit"
	OutcomeSignal  Outcome = "signal"
	OutcomeTimeout Outcome = "timeout"
	OutcomeAbort   Outcome = "abort"
)

// ClassifyOutcome is REQ-TOOL-17.6's pure function. Precedence is part of the
// contract and is deliberately NOT the order the events happened in:
//
//	abort > timeout > exit status
//
// A timed-out command is also a killed command with a non-zero exit status,
// and an aborted one is both. Reporting the exit status would tell the model
// the build failed when in fact the user pressed Ctrl-C.
func ClassifyOutcome(aborted, timedOut bool, exitCode int, signaled bool) Outcome {
	switch {
	case aborted:
		return OutcomeAbort
	case timedOut:
		return OutcomeTimeout
	case signaled:
		// A signal-killed child has no meaningful exit code. Report what
		// happened rather than inventing one.
		return OutcomeSignal
	case exitCode != 0:
		return OutcomeExit
	}
	return OutcomeOK
}

// ExecResult is the outcome of one command.
type ExecResult struct {
	Output     string
	Outcome    Outcome
	ExitCode   int
	Truncated  bool
	TotalBytes int64
	SpillPath  string
	Duration   time.Duration
	// IOErr is a byte-moving failure that did not stop the process: a Stdin
	// read error, a log or spill write error, or Wait reporting an I/O
	// completion failure. Outcome is still classified from the exit status.
	IOErr error
}

// ExecOptions configures Run.
type ExecOptions struct {
	Dir string
	// Timeout of zero means no timeout. REQ-TOOL-06 makes timeout_s optional
	// with NO default: a default wall clock silently kills long builds, and
	// safety comes from process control rather than from a clock.
	Timeout time.Duration
	// MaxBytes bounds the window that reaches the model.
	MaxBytes int
	// KeepHead, when true, keeps the first MaxBytes bytes of the output
	// (head truncation) instead of the last MaxBytes bytes (tail truncation,
	// the default when false).
	KeepHead bool
	// SpillDir enables the full-output spill file.
	SpillDir string
	// LogPath, when set, writes the complete interleaved output to the named
	// file as it arrives, independent of truncation. A relative path is
	// resolved against Dir (or the process working directory when Dir is
	// empty). Missing parent directories are created with mode 0o700 and the
	// file is created or truncated with mode 0o600 before the process starts.
	// When set, LogPath replaces SpillDir for that call: no temporary spill
	// file is created, and ExecResult.SpillPath reports the absolute log path.
	// The SDK never deletes the file.
	LogPath string
	// Stdin, when non-nil, is copied into the child's standard input through
	// a pipe the runner owns. The child's stdin is closed when the reader
	// returns io.EOF. Nil keeps the null device (REQ-TOOL-06).
	Stdin io.Reader
	// Env sets the child's environment. Nil means ReducedEnv(nil)
	// (REQ-SEC-08, same rule as tools.Options.Env): credentials are stripped
	// and PATH, HOME, LANG, TMPDIR and TERM are kept verbatim. A non-nil
	// slice, including an empty one, is used verbatim with no variable added
	// or removed. Pass os.Environ() for the full inherited environment.
	Env []string
	// DrainIdle is how long the output pipe must stay QUIET after the child
	// has exited before draining stops (REQ-TOOL-17.5). Zero means
	// defaultDrainIdle. It is an idle interval, not a deadline: every read
	// re-arms it, so a descendant that keeps writing keeps being drained.
	DrainIdle time.Duration
	// DrainCeiling bounds the whole post-exit drain, so a descendant writing
	// forever cannot hold the tool open forever. Zero means
	// defaultDrainCeiling.
	DrainCeiling time.Duration
}

// The post-exit drain defaults (REQ-TOOL-17.5). The idle interval matches the
// old fixed WaitDelay, so a command with no surviving descendant is bounded
// exactly as before; the difference is that output ARRIVING inside the window
// now re-arms it instead of racing a constant.
const (
	defaultDrainIdle    = 2 * time.Second
	defaultDrainCeiling = 10 * time.Second
)

// Run executes a command through a bash-family shell with full process-group
// lifecycle control.
//
// The pieces that are not obvious, each from REQ-TOOL-17:
//
//   - The command runs in its OWN PROCESS GROUP, and both timeout and context
//     cancellation kill the whole group. Killing only the direct child leaves
//     every grandchild running — a background server, a watch process — long
//     after the agent believes the command is over.
//   - stdout and stderr share ONE PIPE, so they interleave in true write
//     order. Separate captures are prohibited: they produce a transcript in
//     which the error appears before the line that caused it.
//   - The pipe is OURS (os.Pipe), not one exec created, and after the child
//     exits output is drained on a RE-ARMING idle timer rather than a fixed
//     post-exit deadline (REQ-TOOL-17.5). exec closes the pipes it made as
//     soon as Wait returns, which truncates a detached descendant's output at
//     a constant — losing precisely the tail of a background job's log.
//   - Output is truncated from the TAIL unless KeepHead is set (REQ-TOOL-09a).
//
// When ExecOptions.Stdin is non-nil, the reader is copied into the child's
// standard input through a pipe the runner owns. The child's stdin is closed
// when the reader returns io.EOF. A read error other than io.EOF is recorded
// in ExecResult.IOErr. Nil keeps the null device.
func Run(ctx context.Context, command string, opts ExecOptions) (ExecResult, error) {
	shell, args, err := ResolveShell()
	if err != nil {
		return ExecResult{}, err
	}
	opts.Env = effectiveEnv(opts.Env)
	return runArgv(ctx, append(append([]string{shell}, args...), command), opts)
}

// RunArgv is REQ-TOOL-06's structured variant: an argv vector, executed with
// NO SHELL between the caller and the program.
//
// The difference is the entire point. `Run` hands a string to bash, so `;`,
// `$(…)`, backticks, globs and redirection all mean something. Here they do
// not: every element is one argument, verbatim, and a filename containing a
// space or a semicolon reaches the program as itself. A caller that has
// already got its arguments as separate values should never have to quote
// them back into a shell string and hope the quoting is right.
//
// Everything else — process group, timeout, group kill, interleaved output,
// tail truncation (unless KeepHead), spill — is identical, because those are
// properties of running a subprocess and not of how the command was spelled.
//
// When ExecOptions.Stdin is non-nil, the reader is copied into the child's
// standard input through a pipe the runner owns. The child's stdin is closed
// when the reader returns io.EOF. A read error other than io.EOF is recorded
// in ExecResult.IOErr. Nil keeps the null device.
func RunArgv(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error) {
	if len(argv) == 0 {
		return ExecResult{}, errors.New("tools: run_command needs at least one argument")
	}
	// Resolved against PATH here rather than left to exec.Command, so a
	// missing program is a clear error instead of a start failure whose
	// message names only the file.
	prog := argv[0]
	if hasPathSeparator(prog) && !filepath.IsAbs(prog) && opts.Dir != "" {
		// `./script.sh` means "in the directory the command runs in", which
		// is cmd.Dir — the workspace — and not the process working
		// directory that exec.LookPath would consult. Resolved here so the
		// lookup and the run agree on what "." means.
		prog = filepath.Join(opts.Dir, prog)
	}
	opts.Env = effectiveEnv(opts.Env)
	bin, err := lookPathFor(prog, opts.Env)
	if err != nil {
		return ExecResult{}, fmt.Errorf("tools: %q not found on PATH: %w", argv[0], err)
	}
	return runArgv(ctx, append([]string{bin}, argv[1:]...), opts)
}

// lookPathFor resolves a program through the PATH the child will run with.
//
// exec.LookPath reads THIS process's PATH, and the child runs with env: a
// custom Env with a different PATH looked up one binary and ran it with an
// environment that names another. When env sets PATH, a bare name is looked
// up through it — the last PATH entry wins, as it does for exec.Cmd.Env.
// An env that sets no PATH, a nil env, and a name with a path separator keep
// the ordinary lookup. A relative PATH directory is skipped, for the reason
// exec.LookPath refuses a relative result: it resolves against whatever the
// process's working directory happens to be.
func lookPathFor(prog string, env []string) (string, error) {
	path, ok := "", false
	for _, kv := range env {
		k, v, found := strings.Cut(kv, "=")
		if found && (k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH"))) {
			path, ok = v, true
		}
	}
	if !ok || hasPathSeparator(prog) {
		return exec.LookPath(prog)
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if bin, err := exec.LookPath(filepath.Join(dir, prog)); err == nil {
			return bin, nil
		}
	}
	return "", fmt.Errorf("exec: %q: executable file not found in the command's PATH", prog)
}

// runArgv is the shared body. Both entry points reach it with a fully
// resolved argv, so there is exactly one implementation of the process
// lifecycle rather than two that drift.
// openLogFile resolves opts.LogPath (relative to opts.Dir or the working
// directory), creates parent directories with mode 0o700 and opens the file
// with mode 0o600. It returns the absolute path and the open file, or an
// error that should be returned to the caller without starting a process.
func openLogFile(opts ExecOptions) (string, *os.File, error) {
	logAbs := opts.LogPath
	if !filepath.IsAbs(logAbs) {
		base := opts.Dir
		if base == "" {
			var wdErr error
			base, wdErr = os.Getwd()
			if wdErr != nil {
				return "", nil, fmt.Errorf("tools: opening log %s: %w", opts.LogPath, wdErr)
			}
		}
		logAbs = filepath.Join(base, logAbs)
	}
	var absErr error
	logAbs, absErr = filepath.Abs(logAbs)
	if absErr != nil {
		return "", nil, fmt.Errorf("tools: opening log %s: %w", opts.LogPath, absErr)
	}
	if err := os.MkdirAll(filepath.Dir(logAbs), 0o700); err != nil {
		return "", nil, fmt.Errorf("tools: opening log %s: %w", opts.LogPath, err)
	}
	f, err := os.OpenFile(logAbs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("tools: opening log %s: %w", opts.LogPath, err)
	}
	return logAbs, f, nil
}

// classifyWait extracts the exit code, signal flag and any non-exit I/O error
// from the result of cmd.Wait.
func classifyWait(cmd *exec.Cmd, waitErr error) (exitCode int, signaled bool, waitIOErr error) {
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		exitCode = ee.ExitCode()
		// NFR-COMPAT-06: a signal-killed child reports 128+signum on unix,
		// the convention every shell uses, rather than Go's -1 placeholder.
		// On Windows the wait status carries no signal and signalExitCode
		// never claims one.
		if code, ok := signalExitCode(ee); ok {
			exitCode, signaled = code, true
		} else if exitCode == -1 {
			signaled = true
		}
	} else if waitErr != nil {
		// A Wait error that is neither an *exec.ExitError nor nil is an I/O
		// completion failure (e.g. exec.ErrWaitDelay). Record it in IOErr
		// and derive the exit status from ProcessState when available
		// (04-REQ-5.8).
		waitIOErr = waitErr
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
			if exitCode == -1 {
				if code, ok := signalExitCode(&exec.ExitError{ProcessState: cmd.ProcessState}); ok {
					exitCode, signaled = code, true
				} else {
					signaled = true
				}
			}
		}
	}
	return exitCode, signaled, waitIOErr
}

// collectIOErr gathers byte-moving failures from stdin, the log/spill file
// and cmd.Wait into a single error (nil when none occurred).
func collectIOErr(stdinW *os.File, stdinState *stdinCopier, acc *Accumulator, waitIOErr error) error {
	var stdinIOErr error
	if stdinW != nil {
		if sErr := stdinState.finish(); sErr != nil {
			stdinIOErr = fmt.Errorf("tools: reading stdin: %w", sErr)
		}
	}

	var fileIOErr error
	if fErr := acc.fileError(); fErr != nil {
		path := acc.SpillPath()
		if path == "" {
			path = "spill"
		}
		fileIOErr = fmt.Errorf("tools: writing %s: %w", path, fErr)
	}

	return errors.Join(stdinIOErr, fileIOErr, waitIOErr)
}

// setupAccumulator creates and configures the output accumulator, opening the
// log file when LogPath is set or configuring SpillDir otherwise.
func setupAccumulator(opts ExecOptions) (*Accumulator, error) {
	mode := TruncateTail
	if opts.KeepHead {
		mode = TruncateHead
	}
	acc := NewAccumulator(opts.MaxBytes, mode)

	if opts.LogPath != "" {
		logAbs, f, err := openLogFile(opts)
		if err != nil {
			return nil, err
		}
		acc.useFile(f, logAbs)
	} else {
		acc.SpillDir, acc.SpillPrefix = opts.SpillDir, "agentkit-exec"
	}
	return acc, nil
}

// copyStdin copies from src into the write end of the stdin pipe, recording
// any read error in state and closing w through closeOnce when done.
func copyStdin(src io.Reader, w *os.File, closeOnce *sync.Once, state *stdinCopier) {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			_, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				// EPIPE or os.ErrClosed: child closed stdin or
				// exited. Not an error (04-REQ-1.3).
				break
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				// Record the read error BEFORE closing the pipe
				// (04-REQ-1.4).
				state.recordErr(readErr)
			}
			break
		}
	}
	closeOnce.Do(func() { _ = w.Close() })
}

func runArgv(ctx context.Context, argv []string, opts ExecOptions) (ExecResult, error) {
	runCtx, cancel := ctx, context.CancelFunc(nil)
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	}
	if cancel != nil {
		defer cancel()
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = opts.Env
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second // REQ-TOOL-17.3 backstop

	acc, err := setupAccumulator(opts)
	if err != nil {
		return ExecResult{}, err
	}
	defer acc.Close()

	pr, pw, err := os.Pipe() // our pipe for both streams (REQ-TOOL-17.4)
	if err != nil {
		return ExecResult{}, err
	}
	cmd.Stdout, cmd.Stderr = pw, pw

	var (
		stdinW     *os.File
		stdinClose sync.Once
		stdinState stdinCopier
	)
	if opts.Stdin != nil {
		sr, sw, pipeErr := os.Pipe()
		if pipeErr != nil {
			_ = pw.Close()
			_ = pr.Close()
			return ExecResult{}, pipeErr
		}
		cmd.Stdin = sr
		stdinW = sw
	}

	start := time.Now()
	if startErr := cmd.Start(); startErr != nil {
		_ = pw.Close()
		_ = pr.Close()
		if stdinW != nil {
			_ = stdinW.Close()
			_ = cmd.Stdin.(*os.File).Close()
		}
		return ExecResult{}, startErr
	}
	_ = pw.Close()
	if stdinW != nil {
		_ = cmd.Stdin.(*os.File).Close() // parent closes read end
		go copyStdin(opts.Stdin, stdinW, &stdinClose, &stdinState)
	}

	sink := &drainSink{acc: acc}
	copyDone := make(chan struct{})
	go func() { defer close(copyDone); _, _ = io.Copy(sink, pr) }()

	done := make(chan struct{})
	go func() { // kill the process GROUP on cancellation or timeout
		select {
		case <-runCtx.Done():
			killGroup(cmd)
		case <-done:
		}
	}()

	waitErr := waitCmd(cmd)
	close(done)
	elapsed := time.Since(start)
	aborted := ctx.Err() != nil // classify at exit, before the drain
	timedOut := !aborted && runCtx.Err() != nil
	drainAfterExit(runCtx, pr, copyDone, sink, opts.DrainIdle, opts.DrainCeiling)

	if stdinW != nil { // close stdin write end (04-REQ-1.5)
		stdinClose.Do(func() { _ = stdinW.Close() })
	}

	exitCode, signaled, waitIOErr := classifyWait(cmd, waitErr)
	ioErr := collectIOErr(stdinW, &stdinState, acc, waitIOErr)

	return ExecResult{
		Output:     acc.String(),
		Outcome:    ClassifyOutcome(aborted, timedOut, exitCode, signaled),
		ExitCode:   exitCode,
		Truncated:  acc.Truncated(),
		TotalBytes: acc.Total(),
		SpillPath:  acc.SpillPath(),
		Duration:   elapsed,
		IOErr:      ioErr,
	}, nil
}

// drainSink is the accumulator's gate during the post-exit drain.
//
// Two goroutines are involved — the copier reading the pipe and the caller
// reading the result — and the copier may still be blocked in a read on a
// descendant's pipe when the drain gives up. The mutex serialises the two, and
// stop() makes every later write a no-op: an Accumulator write after
// Accumulator.Close would otherwise re-create the spill file that Close just
// released, and the caller's read of the retained window would race the write.
type drainSink struct {
	mu      sync.Mutex
	acc     *Accumulator
	stopped bool
	// lastWrite is the UnixNano of the most recent byte. It is what re-arms
	// the idle timer (REQ-TOOL-17.5), so it is written on every read even when
	// the sink has stopped accumulating.
	lastWrite atomic.Int64
}

func (s *drainSink) Write(p []byte) (int, error) {
	s.lastWrite.Store(time.Now().UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		// Reported as written rather than as an error: the copier is being
		// wound down deliberately, and an error here would only make it log a
		// failure for output nobody is waiting for any more.
		return len(p), nil
	}
	return s.acc.Write(p)
}

// stop closes the sink. It returns once any in-flight write has finished, so
// the caller may read the accumulator afterwards without further locking.
func (s *drainSink) stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

// drainAfterExit is REQ-TOOL-17.5's re-arming idle drain.
//
// The child has exited, but a DETACHED DESCENDANT inherited the write end of
// the pipe and can keep writing for as long as it likes. A fixed post-exit
// deadline cuts that output off at a constant — which is why the requirement
// rules one out — and loses exactly the tail of a background job's log.
//
// So: keep copying while output keeps arriving, re-arming the idle timer at
// every read, and stop only once the pipe has been QUIET for idle. The ceiling
// is the second bound and a different question: idle answers "is anyone still
// writing", ceiling answers "how long will we wait on someone who never
// stops". Cancellation is the third: once the caller has given up, nothing a
// descendant might still write is worth waiting for.
func drainAfterExit(ctx context.Context, pr *os.File, copyDone <-chan struct{}, sink *drainSink, idle, ceiling time.Duration) {
	if idle <= 0 {
		idle = defaultDrainIdle
	}
	if ceiling <= 0 {
		ceiling = defaultDrainCeiling
	}
	idleTimer := time.NewTimer(idle)
	defer idleTimer.Stop()
	ceilingTimer := time.NewTimer(ceiling)
	defer ceilingTimer.Stop()

	for drained := false; !drained; {
		select {
		case <-copyDone:
			// EOF: every writer, descendants included, has let the pipe go.
			drained = true
		case <-idleTimer.C:
			// Re-armed for the REMAINDER of the window rather than a fresh
			// one, so the timer measures quiet time since the last read and
			// not since the last tick.
			if quiet := time.Since(time.Unix(0, sink.lastWrite.Load())); quiet < idle {
				idleTimer.Reset(idle - quiet)
				continue
			}
			drained = true
		case <-ceilingTimer.C:
			drained = true
		case <-ctx.Done():
			drained = true
		}
	}

	// Stop first, then close: after stop the copier cannot touch the
	// accumulator, so whether closing the read end unblocks it (it does
	// wherever a pipe read is pollable) only decides when its goroutine ends,
	// never what the caller sees.
	sink.stop()
	_ = pr.Close()
}

// stdinCopier guards the error recorded by the stdin copier goroutine.
// The copier records a read error under the mutex; the caller reads it after
// Wait and the drain, and marks the state finished so a reader error arriving
// after the call returned is dropped without a data race.
type stdinCopier struct {
	mu       sync.Mutex
	err      error
	finished bool
}

// recordErr records a read error from the copier goroutine. It is a no-op
// after finish has been called.
func (s *stdinCopier) recordErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finished && s.err == nil {
		s.err = err
	}
}

// finish marks the copier state as finished and returns the recorded error.
// After this call, recordErr is a no-op.
func (s *stdinCopier) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	return s.err
}

// ResolveShell is REQ-TOOL-06's fixed ladder.
//
// It never consults $SHELL and never falls back to cmd.exe. $SHELL is the
// user's INTERACTIVE shell — fish, nushell, zsh with a custom rc — and a
// command written for bash is not portable to it. A silent fallback to a
// different dialect produces failures that look like the model wrote bad
// shell.
//
// On Windows the ladder ends in a HARD ERROR naming the paths searched, rather
// than falling through to cmd.
func ResolveShell() (string, []string, error) {
	return resolveShell()
}

// hasPathSeparator reports whether a program name is a path rather than a
// bare name to look up on PATH. Both separators are checked on every platform:
// a model writes `./x` on Windows too.
func hasPathSeparator(name string) bool {
	return strings.ContainsAny(name, `/\`)
}

func lookPathAny(names ...string) (string, bool) {
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p, true
		}
	}
	return "", false
}

// effectiveEnv returns ReducedEnv(nil) for a nil env and the slice verbatim
// otherwise (an empty non-nil slice stays empty). It is the single place
// where ExecOptions.Env's nil-means-reduced rule is applied.
func effectiveEnv(env []string) []string {
	if env == nil {
		return ReducedEnv(nil)
	}
	return env
}

// ReducedEnv strips credentials from the inherited environment while KEEPING
// PATH verbatim (ruling P-47).
//
// Dropping PATH is the obvious reading of "reduced environment" and it breaks
// every command, so it is not what this does. What it removes is credentials,
// which a subprocess has no business reading and which would otherwise be one
// `env` away from any command the model writes (REQ-SEC-08). Three rules, all
// matched on the UPPER-CASED name so `anthropic_api_key` is as gone as
// `ANTHROPIC_API_KEY`:
//
//   - provider PREFIXES, for the keys the SDK itself knows about;
//   - generic SUFFIXES — *_TOKEN, *_SECRET, *_KEY, *_PASSWORD, *_PASS, *_PWD,
//     *_PAT, *_AUTH, *_CREDENTIALS, *_PRIVATE_KEY — because a provider list is
//     only ever the providers someone thought of, and GITHUB_TOKEN or
//     NPM_TOKEN in a developer's shell is at least as valuable to exfiltrate
//     as an Anthropic key;
//   - the bare names TOKEN, API_KEY, SECRET and PASSWORD, which no suffix
//     rule reaches.
//
// A short list is kept unconditionally, checked BEFORE the prefix rule: PATH,
// HOME, LANG, TMPDIR and TERM, which a command needs to run at all, and the
// non-secret provider settings — AWS_PROFILE, AWS_REGION, AWS_DEFAULT_REGION,
// GOOGLE_CLOUD_PROJECT — without which a cloud CLI in the workspace stops
// working while gaining no protection, since they name where to look and not
// what to say.
//
// This is a real but LIMITED protection, and the limit is worth stating: a
// command can still read the keys from any file the agent can read. The
// boundary is the interceptor, not this function.
func ReducedEnv(base []string, extraPrefixes ...string) []string {
	prefixes := append([]string{
		"ANTHROPIC_", "OPENAI_", "GOOGLE_", "GEMINI_", "GROQ_", "DEEPSEEK_",
		"OPENROUTER_", "AZURE_OPENAI_", "AWS_", "MISTRAL_", "COHERE_",
		"TOGETHER_", "FIREWORKS_", "XAI_", "AGENTKIT_",
	}, extraPrefixes...)
	for i, p := range prefixes {
		prefixes[i] = strings.ToUpper(p)
	}
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if !credentialName(name, prefixes) {
			out = append(out, kv)
		}
	}
	return out
}

// credentialSuffixes is the generic half of REQ-SEC-08's rule, matched
// case-insensitively: `github_token` is as much a token as `GITHUB_TOKEN`.
// `_KEY` subsumes `_API_KEY` and `_PRIVATE_KEY`; they are listed anyway so
// the intent survives someone narrowing `_KEY` later.
var credentialSuffixes = []string{
	"_TOKEN", "_SECRET", "_API_KEY", "_KEY", "_PRIVATE_KEY", "_PASSWORD",
	"_PASS", "_PWD", "_PAT", "_AUTH", "_CREDENTIALS",
}

// credentialNames are bare names no suffix rule reaches.
var credentialNames = map[string]bool{"TOKEN": true, "API_KEY": true, "SECRET": true, "PASSWORD": true}

// keptEnv names variables ReducedEnv never drops, whatever they are called.
// The provider entries are the non-secret half of a cloud CLI's config: they
// name where to look, not what to say, and a prefix rule that dropped them
// broke `aws` and `gcloud` in the workspace for no gain.
var keptEnv = map[string]bool{
	"PATH": true, "HOME": true, "LANG": true, "TMPDIR": true, "TERM": true,
	"AWS_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
	"GOOGLE_CLOUD_PROJECT": true,
}

func credentialName(name string, prefixes []string) bool {
	upper := strings.ToUpper(name)
	if keptEnv[upper] {
		return false
	}
	if credentialNames[upper] {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	for _, suf := range credentialSuffixes {
		if strings.HasSuffix(upper, suf) {
			return true
		}
	}
	return false
}
