package ollama_test

import (
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

var hostModel = core.Model{ID: "m"}

// TS-28-4 (property): any trimmed host value resolves by Ollama's CLI rules: no
// scheme means http and port 11434, http:// without a port means 80, https://
// without a port means 443, an explicit port and a path are kept, and
// surrounding whitespace is ignored.
func TestHostResolutionProperty_TS28_4(t *testing.T) {
	r := rand.New(rand.NewSource(28))
	const letters = "abcdefghijklmnopqrstuvwxyz"
	label := func() string {
		b := []byte{letters[r.Intn(len(letters))]}
		for range r.Intn(7) {
			b = append(b, (letters + "0123456789-")[r.Intn(len(letters)+11)])
		}
		return string(b)
	}
	pad := func() string { return []string{"", " ", "\t", "  \n"}[r.Intn(4)] }

	for i := range 500 {
		schemes := []string{"", "http://", "https://"}
		scheme := schemes[r.Intn(len(schemes))]
		host := label()
		if r.Intn(2) == 0 {
			host += "." + label()
		}
		if strings.HasSuffix(host, "ollama.com") {
			continue
		}
		port, explicit := 0, r.Intn(2) == 0
		raw := scheme + host
		if explicit {
			port = 1 + r.Intn(65535)
			raw += fmt.Sprintf(":%d", port)
		}
		path := ""
		if r.Intn(3) == 0 {
			path = "/" + label()
			if r.Intn(2) == 0 {
				path += "/" + label()
			}
		}
		raw = pad() + raw + path + pad()

		wantScheme, defaultPort := "http", 11434
		switch scheme {
		case "http://":
			defaultPort = 80
		case "https://":
			wantScheme, defaultPort = "https", 443
		}
		if !explicit {
			port = defaultPort
		}

		c, err := ollama.New(hostModel, ollama.Options{Host: raw})
		if err != nil {
			t.Fatalf("case %d: New(%q): %v", i, raw, err)
		}
		u, err := url.Parse(c.Host())
		if err != nil {
			t.Fatalf("case %d: Host() = %q does not parse: %v", i, c.Host(), err)
		}
		if u.Scheme != wantScheme || u.Hostname() != host || u.Port() != fmt.Sprint(port) || u.Path != path {
			t.Fatalf("case %d: New(%q).Host() = %q, want %s://%s:%d%s", i, raw, c.Host(), wantScheme, host, port, path)
		}
	}
}

// TS-28-5: documented host examples resolve to the exact base URLs. The rows
// after the first five are the rest of the CLI's rules that the property does
// not generate: bare IP addresses, a missing host and a port with nothing after
// it.
func TestHostExamples_TS28_5(t *testing.T) {
	for in, want := range map[string]string{
		"example.local":   "http://example.local:11434",
		"https://h":       "https://h:443",
		"http://h/proxy":  "http://h:80/proxy",
		"http://h":        "http://h:80",
		"  host:8080  ":   "http://host:8080",
		"127.0.0.1":       "http://127.0.0.1:11434",
		"::1":             "http://[::1]:11434",
		"[::1]":           "http://[::1]:11434",
		"[::1]:8080":      "http://[::1]:8080",
		":8080":           "http://127.0.0.1:8080",
		"http://":         "http://127.0.0.1:80",
		"h:":              "http://h:11434",
		"http://h/":       "http://h:80",
		"https://h:8443/": "https://h:8443",
		"h:0080":          "http://h:80",
	} {
		c, err := ollama.New(hostModel, ollama.Options{Host: in})
		if err != nil {
			t.Errorf("New(%q): %v", in, err)
			continue
		}
		if c.Host() != want {
			t.Errorf("New(%q).Host() = %q, want %q", in, c.Host(), want)
		}
	}
}

// refused checks that New refuses host with an error wrapping ErrInvalidHost
// whose text starts with "ollama:", and returns the text.
func refused(t *testing.T, host string) string {
	t.Helper()
	c, err := ollama.New(hostModel, ollama.Options{Host: host})
	if c != nil || err == nil {
		t.Fatalf("New(%q) = %v, %v; want no client and an error", host, c, err)
	}
	if !errors.Is(err, ollama.ErrInvalidHost) {
		t.Errorf("New(%q) error %q does not wrap ErrInvalidHost", host, err)
	}
	if !strings.HasPrefix(err.Error(), "ollama:") {
		t.Errorf("New(%q) error %q does not start with \"ollama:\"", host, err)
	}
	return err.Error()
}

// TS-28-6: an unsupported scheme, an unparseable value or an out-of-range port
// is ErrInvalidHost. The rows after the first five are values the CLI accepts
// that the provider does not: it sends a request where the CLI would use a
// default, so the provider fails where they would mean different servers.
func TestInvalidHosts_TS28_6(t *testing.T) {
	for _, host := range []string{
		"ftp://h", "http://[::1", "h:0", "h:65536", "http://h:99999",
		"h:abc", "http://h:80:90", "h:-1", "HTTP://h", "http://h?x=1", "http://h/p#frag", "my host", "http://[::1]x",
	} {
		refused(t, host)
	}
}

// TS-28-7: a host with user information is refused and the password is redacted
// from the message.
func TestUserInfoIsRefused_TS28_7(t *testing.T) {
	for _, host := range []string{"http://user:s3cretpw@h:11434", "http://user@h", "user:s3cretpw@h", "https://:s3cretpw@h/p"} {
		msg := refused(t, host)
		if strings.Contains(msg, "s3cretpw") {
			t.Errorf("New(%q) error %q shows the password", host, msg)
		}
		if !strings.Contains(msg, "user information") {
			t.Errorf("New(%q) error %q does not say why", host, msg)
		}
	}
}

// TS-28-8: ollama.com and its subdomains are refused as the unsupported hosted
// service, and a host that merely ends in the same letters is not.
func TestHostedServiceIsRefused_TS28_8(t *testing.T) {
	for _, host := range []string{"ollama.com", "https://ollama.com", "https://api.ollama.com:443", "OLLAMA.COM", "ollama.com.", "http://a.b.ollama.com/x"} {
		if msg := refused(t, host); !strings.Contains(msg, "not supported") {
			t.Errorf("New(%q) error %q does not say the hosted service is not supported", host, msg)
		}
	}
	for _, host := range []string{"notollama.com", "ollama.com.example", "my-ollama.com"} {
		if _, err := ollama.New(hostModel, ollama.Options{Host: host}); err != nil {
			t.Errorf("New(%q): %v", host, err)
		}
	}
}
