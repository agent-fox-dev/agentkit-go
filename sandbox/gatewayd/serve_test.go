package gatewayd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// testPKI is a test CA's pool and a server config with a leaf it signed for
// gw.example.test.
func testPKI(t *testing.T) (*x509.CertPool, *tls.Config) {
	t.Helper()
	key := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	now := time.Now()
	caKey := key()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agentkit test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey := key()
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "gw.example.test"}, DNSNames: []string{"gw.example.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12}
}

// TS-19-32: Serve reaps the backend at start and every interval without
// failing, and returns nil after Shutdown or the listener's error.
// 19-REQ-5.3.
func TestServeReaps_TS19_32(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	reapErr := make([]gwtest.Reaped, 100)
	for i := range reapErr {
		reapErr[i].Err = errorString("reap failed")
	}
	be.SetReaps(reapErr...)
	logs, logger := gwtest.NewLogs()
	srv, err := gatewayd.New(gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}, ReapInterval: 50 * time.Millisecond, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	l := gwtest.NewListener()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	eventually(t, "the first reap", func() bool { return be.Reaps() >= 1 })
	p, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: l.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Create(context.Background(), sandbox.Spec{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if n := be.Reaps(); n < 4 {
		t.Errorf("%d reaps in 400 ms at a 50 ms interval, want at least 4", n)
	}
	if !strings.Contains(logs.String(), "reap failed") {
		t.Error("the log holds no failed reap")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Errorf("Serve after Shutdown = %v, want nil", err)
	}

	srv2, err := gatewayd.New(gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}})
	if err != nil {
		t.Fatal(err)
	}
	l2 := gwtest.NewListener()
	_ = l2.Close()
	if err := srv2.Serve(l2); err == nil {
		t.Error("Serve on a closed listener returned nil")
	}
	_ = srv2.Shutdown(ctx)
}

// errorString is an error with a fixed text.
type errorString string

func (e errorString) Error() string { return string(e) }

// TS-19-33: with Config.TLS the server serves TLS to an https client, and
// relayed messages up to MaxMessageBytes pass. 19-REQ-5.3, 19-REQ-2.4.
func TestServeTLS_TS19_33(t *testing.T) {
	pool, srvTLS := testPKI(t)
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), TLS: srvTLS, NetworkModes: unrestricted})
	p, err := gateway.Dial(context.Background(), "https://gw.example.test",
		gateway.Options{Token: gwtest.Token, TLSConfig: &tls.Config{RootCAs: pool}, Dialer: env.Listener.Dial})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	sb := create(t, p, unrestrictedSpec)
	in, err := json.Marshal(map[string]any{"path": "big.bin", "content": strings.Repeat("x", 12<<20)})
	if err != nil {
		t.Fatal(err)
	}
	if r := toolOf(t, sb, "write_file").Execute(context.Background(), in); !r.OK {
		t.Fatalf("write_file of 12 MiB: %+v", r.Error)
	}
	fi, err := os.Stat(filepath.Join(sb.Spec().Workdir, "big.bin"))
	if err != nil || fi.Size() != 12<<20 {
		t.Errorf("the written file: %v, %v; want 12 MiB", fi, err)
	}
	if _, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: env.Listener.Dial}); err == nil ||
		!strings.Contains(err.Error(), gwtest.URL) {
		t.Errorf("a plaintext Dial to the TLS gateway gave %v, want a failure naming its URL", err)
	}
}
