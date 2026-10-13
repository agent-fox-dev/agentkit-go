//go:build !unix

package landlock

import (
	"errors"
	"io"
)

// startProcess has no Landlock agent to start off Unix; New refuses first.
func startProcess(string, []string, []string, string) (*agentProc, io.WriteCloser, io.ReadCloser, error) {
	return nil, nil, nil, errors.ErrUnsupported
}
