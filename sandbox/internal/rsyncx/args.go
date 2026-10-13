//go:build !windows

package rsyncx

import (
	"fmt"
	"strings"
	"sync"
)

// ParseArgs reads a server-side argument list. Every option token is checked
// with the library's own option parser; one it does not know is an
// *ArgError naming it.
func ParseArgs(args []string) (Parsed, error) {
	var p Parsed
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" {
			p.Operands = append(p.Operands, a)
			continue
		}
		// Checked one token at a time, so a refusal names it; with --server,
		// which server-only options such as --sender require.
		if _, err := newClient([]string{"--server", a}, false); err != nil {
			return p, &ArgError{Token: a, Reason: "the rsync library does not know it: " + err.Error()}
		}
		p.Options = append(p.Options, a)
		switch {
		case a == "--server":
			p.Server = true
		case a == "--sender":
			p.Sender = true
		case a == "--delete":
			p.Delete = true
		case a == "--recursive":
			p.Recursive = true
		case a == "--links":
			p.Links = true
		case a == "--times":
			p.Times = true
		case a == "--perms":
			p.Perms = true
		case strings.HasPrefix(a, "--exclude="):
			p.Excludes = append(p.Excludes, strings.TrimPrefix(a, "--exclude="))
		case !strings.HasPrefix(a, "--"):
			p.Recursive = p.Recursive || strings.ContainsRune(a, 'r')
			p.Links = p.Links || strings.ContainsRune(a, 'l')
			p.Times = p.Times || strings.ContainsRune(a, 't')
			p.Perms = p.Perms || strings.ContainsRune(a, 'p')
		}
	}
	return p, nil
}

// allowlist is the set of option tokens this module produces: the long
// options and the letters of the short-option bundles ServerArgs emits for
// every direction, with and without delete.
var allowlist = sync.OnceValues(func() (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, del := range []bool{false, true} {
		for _, sends := range []bool{false, true} {
			args, err := ServerArgs(ClientArgs(del), sends, ".")
			if err != nil {
				return nil, err
			}
			p, err := ParseArgs(args)
			if err != nil {
				return nil, err
			}
			for _, o := range p.Options {
				if strings.HasPrefix(o, "--") {
					allowed[o] = true
					continue
				}
				for _, c := range o[1:] {
					allowed["-"+string(c)] = true
				}
			}
		}
	}
	return allowed, nil
})

// CheckServerArgs checks the agent's argument list for a transfer in
// direction d whose start frame named startPath: every option must be one the
// library knows and one this module produces, the sender flag must be present
// exactly for EXPORT, and a path operand may name only the module root or
// startPath.
func CheckServerArgs(args []string, d Direction, startPath string) error {
	p, err := ParseArgs(args)
	if err != nil {
		return err
	}
	allowed, err := allowlist()
	if err != nil {
		return err
	}
	for _, o := range p.Options {
		if strings.HasPrefix(o, "--") {
			if !allowed[o] {
				return &ArgError{Token: o, Reason: "not an option this module uses"}
			}
			continue
		}
		for _, c := range o[1:] {
			if !allowed["-"+string(c)] {
				return &ArgError{Token: o, Reason: fmt.Sprintf("-%c is not an option this module uses", c)}
			}
		}
	}
	if p.Sender != (d == DirExport) {
		return &ArgError{Token: "--sender", Reason: "the sender flag must be present exactly for EXPORT"}
	}
	for _, op := range p.Operands {
		if op != "." && op != "./" && op != startPath && op != startPath+"/" {
			return &ArgError{Token: op, Reason: "a path operand may name only the module root or the start path"}
		}
	}
	return nil
}
