package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TS-19-11: for https the client clones the caller's TLS config, falls back
// to the system roots, uses the URL host as ServerName and carries messages
// up to MaxMessageBytes. 19-REQ-2.4.
func TestTLS_TS19_11(t *testing.T) {
	pki := newTestPKI(t)
	f := newFakeGW(t, pki.server)
	const url = "https://gw.example.test"
	cfg := &tls.Config{RootCAs: pki.pool}
	snap := cfg.Clone()
	p, err := Dial(context.Background(), url, Options{Token: testToken, TLSConfig: cfg, Dialer: f.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !reflect.DeepEqual(cfg, snap) || cfg.ServerName != "" {
		t.Error("Dial modified the caller's TLS config")
	}
	sb := mustCreate(t, p)
	in, err := json.Marshal(map[string]any{"path": "big.bin", "content": strings.Repeat("x", 12<<20)})
	if err != nil {
		t.Fatal(err)
	}
	_ = tool(t, sb, "write_file").Execute(context.Background(), in)
	if n := len(f.agent.LastInput()); n < 12<<20 {
		t.Errorf("the agent received %d input bytes, want the 12 MiB input", n)
	}
	var v uint16
	f.set(func() { v = f.tlsVersion })
	if v < tls.VersionTLS12 {
		t.Errorf("the gateway saw TLS version %#x, want at least TLS 1.2", v)
	}
	if _, err := Dial(context.Background(), url, Options{Token: testToken, Dialer: f.Dial}); err == nil || !strings.Contains(err.Error(), url) {
		t.Errorf("a Dial trusting only the system roots gave %v, want a failure naming %s", err, url)
	}
}
