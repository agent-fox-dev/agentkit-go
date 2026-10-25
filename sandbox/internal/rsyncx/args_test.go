//go:build !windows

package rsyncx

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestClientArgsDeclareDeleteOnlyWhenAsked(t *testing.T) {
	if got := ClientArgs(false); !slices.Equal(got, []string{"-r", "-l", "-t", "-p"}) {
		t.Errorf("ClientArgs(false) = %q", got)
	}
	if got := ClientArgs(true); !slices.Contains(got, "--delete") {
		t.Errorf("ClientArgs(true) = %q, want --delete declared", got)
	}
}

// The server's arguments name the sender exactly when it sends, keep a
// declared --delete before the operands, and parse back to what was asked.
func TestServerArgsParseBackToTheTransfer(t *testing.T) {
	for _, c := range []struct {
		del, sends bool
	}{{false, false}, {false, true}, {true, false}, {true, true}} {
		args, err := ServerArgs(ClientArgs(c.del), c.sends, "sub/")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(args[len(args)-2:], []string{".", "sub/"}) {
			t.Errorf("ServerArgs(del %v, sends %v) = %q, want it to end with the operands", c.del, c.sends, args)
		}
		p, err := ParseArgs(args)
		if err != nil {
			t.Fatal(err)
		}
		if p.Sender != c.sends || p.Delete != c.del || !p.Recursive || !p.Links || !p.Times || !p.Perms {
			t.Errorf("ParseArgs(%q) = %+v", args, p)
		}
		if !slices.Equal(p.Operands, []string{".", "sub/"}) {
			t.Errorf("Operands = %q", p.Operands)
		}
	}
}

func TestParseArgsReadsLongOptionsAndExcludes(t *testing.T) {
	p, err := ParseArgs([]string{"--recursive", "--links", "--times", "--perms", "--exclude=a b", "-"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Recursive || !p.Links || !p.Times || !p.Perms || !slices.Equal(p.Excludes, []string{"a b"}) || !slices.Equal(p.Operands, []string{"-"}) {
		t.Errorf("ParseArgs = %+v", p)
	}
}

func TestParseArgsRefusesAnOptionTheLibraryDoesNotKnow(t *testing.T) {
	for _, tok := range []string{"--bogus", "-z", "-rz"} {
		_, err := ParseArgs([]string{"--server", tok})
		var ae *ArgError
		if !errors.As(err, &ae) || ae.Token != tok || !strings.Contains(err.Error(), "does not know") {
			t.Errorf("ParseArgs(%q) = %v, want an ArgError naming it", tok, err)
		}
	}
}

func TestCheckServerArgsAcceptsWhatThisModuleProduces(t *testing.T) {
	for _, c := range []struct {
		d       Direction
		operand string
	}{{DirSync, "."}, {DirExport, "."}, {DirExport, "sub/"}, {DirExport, "f.txt"}} {
		for _, del := range []bool{false, true} {
			args, err := ServerArgs(ClientArgs(del), c.d == DirExport, c.operand)
			if err != nil {
				t.Fatal(err)
			}
			if err := CheckServerArgs(args, c.d, strings.TrimSuffix(c.operand, "/")); err != nil {
				t.Errorf("CheckServerArgs(%q, %v) = %v", args, c.d, err)
			}
		}
	}
}

func TestCheckServerArgsRefusesWhatThisModuleDoesNotProduce(t *testing.T) {
	sync := func(extra ...string) []string { return append([]string{"--server", "-ltpr"}, extra...) }
	for _, c := range []struct {
		args  []string
		d     Direction
		token string
	}{
		{sync("--bogus", ".", "."), DirSync, "--bogus"},
		{sync("--exclude=x", ".", "."), DirSync, "--exclude=x"},
		{sync("-v", ".", "."), DirSync, "-v"},
		{sync(".", "."), DirExport, "--sender"},
		{append(sync(".", "."), "--sender"), DirSync, "--sender"},
		{sync(".", "../etc"), DirSync, "../etc"},
		{sync(".", "/etc"), DirSync, "/etc"},
	} {
		err := CheckServerArgs(c.args, c.d, "start")
		var ae *ArgError
		if !errors.As(err, &ae) || ae.Token != c.token {
			t.Errorf("CheckServerArgs(%q, %v) = %v, want an ArgError naming %q", c.args, c.d, err, c.token)
		}
	}
}
