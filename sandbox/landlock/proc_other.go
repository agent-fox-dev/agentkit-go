//go:build !linux

package landlock

import (
	"errors"
	"syscall"
)

// otherTable has no process table to read; New refuses before it is used.
type otherTable struct{}

func defaultProcTable() procTable { return otherTable{} }

func (otherTable) list() ([]procEntry, error)       { return nil, errors.ErrUnsupported }
func (otherTable) lookup(int) (uint64, bool, error) { return 0, false, errors.ErrUnsupported }
func (otherTable) bootID() (string, error)          { return "", errors.ErrUnsupported }
func (otherTable) kill(int, syscall.Signal) error   { return errors.ErrUnsupported }
