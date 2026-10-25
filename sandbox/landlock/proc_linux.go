package landlock

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// linuxTable is the process table over /proc.
type linuxTable struct{}

func defaultProcTable() procTable { return linuxTable{} }

func (linuxTable) list() ([]procEntry, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []procEntry
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, err := readStat(pid); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (linuxTable) lookup(pid int) (uint64, bool, error) {
	p, err := readStat(pid)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	// A zombie has exited: its entry stays only until its parent reaps it,
	// and under an init that does not reap orphans that is never.
	if p.State == 'Z' {
		return 0, false, nil
	}
	return p.Start, true, nil
}

func (linuxTable) bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

func (linuxTable) kill(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }

// readStat parses /proc/<pid>/stat after the last ')', where field 3 is the
// state, field 6 the session id and field 22 the start time.
func readStat(pid int) (procEntry, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procEntry{}, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procEntry{}, fmt.Errorf("/proc/%d/stat: no command name", pid)
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	if len(f) < 20 || len(f[0]) == 0 {
		return procEntry{}, fmt.Errorf("/proc/%d/stat: %d fields", pid, len(f)+2)
	}
	sid, err := strconv.Atoi(f[3])
	if err != nil {
		return procEntry{}, err
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return procEntry{}, err
	}
	return procEntry{PID: pid, Session: sid, State: f[0][0], Start: start}, nil
}
