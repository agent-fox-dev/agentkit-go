package landlock

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-16: Capabilities report a landlock confinement with two network
// modes, no resources, no image or workdir, and Landlock. 18-REQ-3.1.
func TestCapabilities_TS18_16(t *testing.T) {
	c0 := newTestProvider(t, Options{}).Capabilities()
	c1 := newTestProvider(t, Options{ExtraRO: []string{t.TempDir()}, ExtraRW: []string{t.TempDir()}}).Capabilities()
	for _, c := range []sandbox.Capabilities{c0, c1} {
		if c.Provider != "landlock" || c.Isolation != sandbox.IsolationConfinement ||
			!slices.Equal(c.Network, []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}) ||
			c.Resources != (sandbox.ResourceCaps{}) || c.Image || c.Workdir || !c.Landlock {
			t.Errorf("Capabilities = %+v", c)
		}
	}
	if !reflect.DeepEqual(c0, c1) {
		t.Error("Capabilities depend on Options")
	}
}

// TS-18-17: Notes state plainly what the confinement does not protect.
// 18-REQ-3.2.
func TestCapabilitiesNotes_TS18_17(t *testing.T) {
	n := newTestProvider(t, Options{}).Capabilities().Notes
	for _, s := range []string{"not an isolation", "UDP", "Multipath TCP", "Unix socket", "D-Bus", "ssh-agent", "signal", "new session", "host paths"} {
		if !strings.Contains(n, s) {
			t.Errorf("Notes lack %q: %s", s, n)
		}
	}
}
