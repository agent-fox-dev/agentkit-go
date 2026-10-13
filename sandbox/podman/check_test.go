package podman

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// refused asserts that Create refuses spec with a SpecError whose Field
// satisfies field, before any engine command.
func refused(t *testing.T, spec sandbox.Spec, field func(string) bool) {
	t.Helper()
	_ = refusal(t, spec, field)
}

// refusal is refused, returning the SpecError (nil after a failure).
func refusal(t *testing.T, spec sandbox.Spec, field func(string) bool) *sandbox.SpecError {
	t.Helper()
	f := newFake(t)
	_, err := testProvider(f, Options{}).Create(context.Background(), spec)
	var se *sandbox.SpecError
	if !errors.As(err, &se) || !field(se.Field) || !errors.Is(err, sandbox.ErrUnsupported) || se.Provider != "podman" {
		t.Errorf("Create(%+v) = %v, want a podman SpecError on the right field", spec, err)
		return nil
	}
	if n := len(f.recorded()); n != 0 {
		t.Errorf("Create(%+v) ran %d engine commands before refusing", spec, n)
	}
	return se
}

// accepted asserts that Create passes the checks for spec.
func accepted(t *testing.T, spec sandbox.Spec) {
	t.Helper()
	f := newFake(t)
	s, err := testProvider(f, Options{}).Create(context.Background(), spec)
	if err != nil {
		t.Errorf("Create(%+v) = %v, want it accepted", spec, err)
		return
	}
	_ = s.Close(context.Background())
}

func is(want string) func(string) bool { return func(f string) bool { return f == want } }

// TS-17-17: any Spec.Provider key is refused, naming the first key in
// sorted order. 17-REQ-3.2.
func TestRefuseProviderKeys_TS17_17(t *testing.T) {
	se := refusal(t, sandbox.Spec{Image: "img:1", Provider: map[string]any{"zeta": 1, "alpha": 2}},
		func(f string) bool { return strings.Contains(f, "Provider") })
	if se != nil && !strings.Contains(se.Field+se.Reason, "alpha") {
		t.Errorf("the refusal %v does not name alpha", se)
	}
}

// TS-17-18: reserved, empty and malformed label keys and control-character
// values are refused. 17-REQ-3.3.
func TestRefuseLabels_TS17_18(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"agentkit.sandbox.expires", "x"}, {"agentkit.sandbox.owner", "x"}, {"", "x"}, {"a=b", "x"},
		{"a b", "x"}, {"a\tb", "x"}, {"a\x01", "x"}, {"ok", "v\nw"},
	} {
		spec := sandbox.Spec{Image: "img:1", Labels: map[string]string{c.key: c.value}}
		se := refusal(t, spec, is("Labels"))
		if se != nil && !strings.Contains(se.Error(), strings.Trim(strconv.Quote(c.key), `"`)) {
			t.Errorf("the refusal %q does not name the key %q", se, c.key)
		}
	}
	accepted(t, sandbox.Spec{Image: "img:1", Labels: map[string]string{"team": "core", "agentkit.sandboxes": "x"}})
}

// TS-17-19: an effective Workdir that is relative, unclean, a system or
// tmp path, or holds a comma or control character is refused. 17-REQ-3.4.
func TestRefuseWorkdir_TS17_19(t *testing.T) {
	for _, w := range []string{"workspace", "/workspace/", "/a/../workspace", "/", "/tmp", "/tmp/w", "/proc/x", "/sys/x", "/dev/x", "/work,space", "/work\x01"} {
		refused(t, sandbox.Spec{Image: "img:1", Workdir: w}, is("Workdir"))
	}
	for _, w := range []string{"", "/home/agentkit/ws", "/tmpfoo"} {
		accepted(t, sandbox.Spec{Image: "img:1", Workdir: w})
	}
}

// TS-17-20: an image reference that starts with a dash or holds whitespace
// or a control character is refused. 17-REQ-3.5.
func TestRefuseImage_TS17_20(t *testing.T) {
	for _, img := range []string{"-it", "--privileged", "img name", "img\tx", "img\n", "img\x7f"} {
		refused(t, sandbox.Spec{Image: img}, is("Image"))
	}
}

// TS-17-21: HOME or TMPDIR outside /tmp and the workspace is refused with a
// SpecError naming the variable. 17-REQ-3.6.
func TestRefuseHomeTmpdir_TS17_21(t *testing.T) {
	for _, c := range []struct{ name, value string }{
		{"HOME", "home"}, {"HOME", "/home/agentkit"}, {"HOME", "/var/tmp"}, {"TMPDIR", "/workspacefoo"}, {"TMPDIR", "/tmpx"},
	} {
		refused(t, sandbox.Spec{Image: "img:1", Env: map[string]string{c.name: c.value}},
			func(f string) bool { return strings.Contains(f, "Env") && strings.Contains(f, c.name) })
	}
	for _, env := range []map[string]string{{"HOME": "/tmp/h"}, {"HOME": "/workspace/.home"}, {"TMPDIR": "/tmp"}, {"TMPDIR": "/workspace"}} {
		accepted(t, sandbox.Spec{Image: "img:1", Env: env})
	}
}
