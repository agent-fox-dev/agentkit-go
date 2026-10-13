package podman

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"time"
)

// waitDelay bounds how long the engine process's stderr may stay open after
// it has exited, so a descendant holding it cannot hold Wait.
const waitDelay = 5 * time.Second

// engineProc is the real engine process.
type engineProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *os.File
	stderr tailBuffer
	done   chan struct{}
	code   int // set before done is closed
}

// startProc starts argv with env in its own process group: stdin is a
// pipe, stdout a private os.Pipe whose write end only the child holds, and
// stderr is drained concurrently into a tail buffer, so the child never
// blocks on it.
func startProc(argv, env []string) (*engineProc, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.SysProcAttr = sysProcAttr()
	cmd.WaitDelay = waitDelay
	p := &engineProc{cmd: cmd, done: make(chan struct{})}
	cmd.Stderr = &p.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, errors.Join(err, stdin.Close())
	}
	cmd.Stdout = w
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, stdin.Close(), r.Close(), w.Close())
	}
	_ = w.Close() // the child holds the only write end
	p.stdin, p.stdout = stdin, r
	go func() {
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	return p, nil
}

func (p *engineProc) Stdin() io.WriteCloser { return p.stdin }
func (p *engineProc) Stdout() io.ReadCloser { return p.stdout }
func (p *engineProc) Done() <-chan struct{} { return p.done }
func (p *engineProc) StderrTail() string    { return p.stderr.String() }

func (p *engineProc) ExitCode() int {
	<-p.done
	return p.code
}

func (p *engineProc) KillGroup() error { return killGroup(p.cmd.Process) }
