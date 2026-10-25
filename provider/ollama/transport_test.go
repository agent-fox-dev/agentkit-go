package ollama_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// signRequests makes the SDK sign every request, as it does when OLLAMA_AUTH is
// set and its key is under $HOME.
func signRequests(t *testing.T) {
	t.Helper()
	testutil.Hermetic(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	testutil.WriteTree(t, os.Getenv("HOME"), map[string]string{".ollama/id_ed25519": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	t.Setenv("OLLAMA_AUTH", "1")
}

// checkCredentialFree fails the test if a request carries an Authorization
// header, or a secret in its URL, headers or body.
func checkCredentialFree(t *testing.T, srv *fakeServer, secrets []string) {
	t.Helper()
	if srv.Count() == 0 {
		t.Fatal("the server received no request")
	}
	for i, r := range srv.Requests() {
		all := fmt.Sprint(r.Path, r.RawQuery, r.Header, string(r.Body))
		for _, s := range append(secrets, "Authorization") {
			if strings.Contains(all, s) {
				t.Errorf("request %d carries %q", i, s)
			}
		}
	}
}

// No Authorization header or other provider's key reaches the server, even when
// the SDK signs the request; a header the embedder's own transport adds does.
func TestNoCredentialIsSentButTheEmbeddersHeadersAre(t *testing.T) {
	secrets := setSecrets(t)
	srv := newServer(t, okTurn())
	if o := run(t, newClient(t, srv, testModel()), userRequest("hi")); o.err != nil {
		t.Fatal(o.err)
	}
	checkCredentialFree(t, srv, secrets)

	if runtime.GOOS != "windows" { // the SDK finds its key through $HOME
		signRequests(t)
		signed := newServer(t, okTurn())
		run(t, newClient(t, signed, testModel()), userRequest("hi"))
		if q := signed.Requests()[0].RawQuery; !strings.HasPrefix(q, "ts=") {
			t.Fatalf("the query is %q: the SDK did not take its signing path", q)
		}
		checkCredentialFree(t, signed, secrets)
	}

	proxied := newServer(t, okTurn())
	run(t, newClient(t, proxied, testModel(), func(o *ollama.Options) {
		o.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r = r.Clone(r.Context())
			r.Header.Set("Authorization", "Bearer proxy-token")
			return http.DefaultTransport.RoundTrip(r)
		})}
	}), userRequest("hi"))
	if h := proxied.Requests()[0].Header; h.Get("Authorization") != "Bearer proxy-token" {
		t.Errorf("headers %v: the embedder's Authorization should reach the server", h)
	}
}

// The provider sends through a copy of the embedder's client, so its Timeout
// still cuts a request that does not answer.
func TestTheEmbeddersClientSettingsAreKept(t *testing.T) {
	srv := newServer(t, reply{status: http.StatusOK, hold: true})
	start := time.Now()
	o := run(t, newClient(t, srv, testModel(), func(o *ollama.Options) {
		o.HTTPClient = &http.Client{Timeout: 100 * time.Millisecond}
	}), userRequest("hi"))
	if o.err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("err = %v after %v, want the client's timeout to end the turn", o.err, time.Since(start))
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
