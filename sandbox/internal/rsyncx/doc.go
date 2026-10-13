// Package rsyncx is the only package that imports github.com/gokrazy/rsync.
// The host's attach code and the agent share it.
//
// The library can neither apply exclude rules safely nor keep a rule's paths
// from deletion (docs/errata/14_sandbox_rsync_sync.md), so it is used for one
// thing only: moving the files of a tree that already holds exactly the
// transfer set. The sending side runs a server over a filtered, read-only view
// of its tree; the receiving side runs a client that writes into its
// directory. Rules and deletion are AgentKit's own.
package rsyncx

import "fmt"

// Direction is a transfer's direction.
type Direction int

const (
	// DirSync moves host files into the workspace.
	DirSync Direction = 1
	// DirExport moves workspace files to the host.
	DirExport Direction = 2
)

const (
	// MaxDataChunk is the most rsync data one data frame carries.
	MaxDataChunk = 1 << 20
	// MaxRuleBytes caps the combined rules, and a sender's transfer set.
	MaxRuleBytes = 8 << 20
)

// Module describes the agent's side of a transfer: on SYNC it receives into
// Root, which is writable; on EXPORT it serves Root read-only.
type Module struct {
	Root     string
	Writable bool
}

// ModulesFor is the agent's one module for a transfer in direction d over
// the workspace ws: writable for SYNC, where the agent's client receives
// into it, and read-only for EXPORT, where the agent's server sends a
// read-only view of it. The agent dispatches on it.
func ModulesFor(ws string, d Direction) []Module {
	return []Module{{Root: ws, Writable: d == DirSync}}
}

// ClientArgs are the options of every transfer: recursion, links, times
// (which make the quick check, and so incremental re-syncs, work) and
// permissions. With del, --delete is added as the declared intent; AgentKit
// deletes, and the library never sees it.
func ClientArgs(del bool) []string {
	args := []string{"-r", "-l", "-t", "-p"}
	if del {
		args = append(args, "--delete")
	}
	return args
}

// Parsed is a server-side argument list, read by ParseArgs.
type Parsed struct {
	Recursive, Links, Times, Perms bool
	Delete, Sender                 bool
	Excludes                       []string
	// Options are the option tokens, in order.
	Options []string
	// Operands are the path operands, in order.
	Operands []string
}

// ArgError is an argument the agent refuses.
type ArgError struct {
	Token  string
	Reason string
}

func (e *ArgError) Error() string { return fmt.Sprintf("rsync argument %q: %s", e.Token, e.Reason) }
