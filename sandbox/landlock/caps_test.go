package landlock

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// They never depend on Options, and Notes say what the confinement does not protect.
func TestCapabilitiesReportAConfinement(t *testing.T) {
	c := newTestProvider(t, Options{}).Capabilities()
	if c.Provider != "landlock" || c.Isolation != sandbox.IsolationConfinement || !c.Landlock || c.Image || c.Workdir ||
		!slices.Equal(c.Network, []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}) || c.Resources != (sandbox.ResourceCaps{}) {
		t.Errorf("Capabilities = %+v", c)
	}
	if c2 := newTestProvider(t, Options{ExtraRO: []string{t.TempDir()}, ExtraRW: []string{t.TempDir()}}).Capabilities(); !reflect.DeepEqual(c, c2) {
		t.Error("Capabilities depend on Options")
	}
	for _, s := range []string{"not an isolation", "UDP", "Multipath TCP", "Unix socket", "D-Bus", "ssh-agent", "signal", "new session", "host paths", "sandbox/podman"} {
		if !strings.Contains(c.Notes, s) {
			t.Errorf("Notes lack %q", s)
		}
	}
}
