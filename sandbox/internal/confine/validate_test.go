package confine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A --workdir or --landlock-rw that is /, or is or contains a system
// directory (through a symlink or relative to the working directory too),
// and a relative --landlock-ro or --landlock-rw are refused with a usage
// error naming the flag; anything else passes.
func TestValidateRefusesAFlagThatWidensTheBoundary(t *testing.T) {
	d := dirs(t, "w")
	usr := filepath.Join(d["base"], "usr")
	if err := os.Symlink("/usr", usr); err != nil {
		t.Fatal(err)
	}
	t.Chdir("/")
	for _, c := range []struct {
		in   Inputs
		flag string // "" when accepted
	}{
		{Inputs{Workdir: "/"}, "--workdir"},
		{Inputs{Workdir: "."}, "--workdir"},
		{Inputs{Workdir: "etc"}, "--workdir"},
		{Inputs{Workdir: usr}, "--workdir"},
		{Inputs{Workdir: d["w"], RW: []string{"/lib"}}, "--landlock-rw"},
		{Inputs{Workdir: d["w"], RW: []string{"rel"}}, "--landlock-rw"},
		{Inputs{Workdir: d["w"], RO: []string{"rel"}}, "--landlock-ro"},
		{Inputs{Workdir: d["w"], RW: []string{d["base"]}, RO: []string{"/"}}, ""},
		{Inputs{Workdir: "/usr/local/agentkit-missing"}, ""},
	} {
		err := validate(c.in)
		var ue *UsageError
		switch {
		case c.flag == "" && err != nil:
			t.Errorf("%+v: %v", c.in, err)
		case c.flag != "" && (!errors.As(err, &ue) || ue.Flag != c.flag || !strings.HasPrefix(err.Error(), c.flag+": ")):
			t.Errorf("%+v: %v, want a usage error naming %s", c.in, err, c.flag)
		}
	}
}
