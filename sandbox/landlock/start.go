package landlock

import (
	"net"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// defaultStart starts the agent process and adapts its pipes into the
// connection attach dials over. It is never given Create's context, so the
// agent outlives it.
func defaultStart(req startReq) (*agentProc, net.Conn, error) {
	h, stdin, stdout, err := startProcess(req.bin, req.args, req.env, req.dir)
	if err != nil {
		return nil, nil, err
	}
	return h, stdioconn.New(stdout, stdin), nil
}
