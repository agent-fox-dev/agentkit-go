//go:build unix

package landlock

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// startProcess starts bin with args and env in dir, as the leader of a new
// session (and so of its own process group), with stdin and stdout pipes and
// stderr drained into the handle's tail. It is not tied to any context; a
// goroutine waits on it and records its exit.
func startProcess(bin string, args, env []string, dir string) (*agentProc, io.WriteCloser, io.ReadCloser, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, errors.Join(err, stdin.Close())
	}
	cmd.Stdout = w
	h := newAgentProc(0, nil)
	cmd.Stderr = h.tail
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, errors.Join(err, stdin.Close(), r.Close(), w.Close())
	}
	_ = w.Close() // the child holds the only write end
	h.pid = cmd.Process.Pid
	h.killFn = func() error {
		if err := syscall.Kill(-h.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	go func() {
		_ = cmd.Wait()
		ws, _ := cmd.ProcessState.Sys().(syscall.WaitStatus)
		if ws.Signaled() {
			h.setExit(-1, true, ws.Signal())
		} else {
			h.setExit(cmd.ProcessState.ExitCode(), false, 0)
		}
	}()
	return h, stdin, r, nil
}
