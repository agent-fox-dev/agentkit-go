package ollama_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// A host value resolves by Ollama's CLI rules: trimmed; no scheme means http
// and port 11434, http:// alone 80 and https:// alone 443; a port, a path and
// an IP address are kept; no host is 127.0.0.1.
func TestAHostResolvesByTheOllamaCLIRules(t *testing.T) {
	for in, want := range map[string]string{
		"":                             ollama.DefaultHost,
		"example.local":                "http://example.local:11434",
		"https://h":                    "https://h:443",
		"http://h":                     "http://h:80",
		"http://h/proxy":               "http://h:80/proxy",
		"  host:8080  ":                "http://host:8080",
		"\thttps://a-1.b2:65535/x/y\n": "https://a-1.b2:65535/x/y",
		"h:1":                          "http://h:1",
		"127.0.0.1":                    "http://127.0.0.1:11434",
		"::1":                          "http://[::1]:11434",
		"[::1]":                        "http://[::1]:11434",
		"[::1]:8080":                   "http://[::1]:8080",
		":8080":                        "http://127.0.0.1:8080",
		"http://":                      "http://127.0.0.1:80",
		"h:":                           "http://h:11434",
		"http://h/":                    "http://h:80",
		"https://h:8443/":              "https://h:8443",
		"h:0080":                       "http://h:80",
		"notollama.com":                "http://notollama.com:11434",
		"ollama.com.example":           "http://ollama.com.example:11434",
	} {
		c, err := ollama.New(core.Model{ID: "m"}, ollama.Options{Host: in, Getenv: func(string) string { return "" }})
		if err != nil || c.Host() != want {
			t.Errorf("New(%q) = %v, Host %v; want %q", in, err, c, want)
		}
	}
}

// A value the provider does not accept fails New with ErrInvalidHost and says
// why, where the CLI would guess; a password in it is never shown.
func TestAnInvalidHostIsRefusedSayingWhy(t *testing.T) {
	for _, tc := range []struct{ host, why string }{
		{"ftp://h", "scheme"},
		{"HTTP://h", "scheme"},
		{"http://[::1", "cannot be parsed"},
		{"[1.2.3.4]", "not an IPv6 address"},
		{"http://[::1]x", "cannot be parsed"},
		{"h:0", "port"},
		{"h:65536", "port"},
		{"http://h:99999", "port"},
		{"h:abc", "port"},
		{"h:-1", "port"},
		{"http://h:80:90", "cannot be parsed"},
		{"http://h?x=1", "query"},
		{"http://h/p#frag", "fragment"},
		{"my host", "space"},
		{`a\b`, "backslash"},
		{"http://user:s3cretpw@h:11434", "user information"},
		{"http://user@h", "user information"},
		{"user:s3cretpw@h", "user information"},
		{"https://:s3cretpw@h/p", "user information"},
		{"ollama.com", "not supported"},
		{"https://api.ollama.com:443", "not supported"},
		{"OLLAMA.COM", "not supported"},
		{"ollama.com.", "not supported"},
		{"http://a.b.ollama.com/x", "not supported"},
	} {
		c, err := ollama.New(core.Model{ID: "m"}, ollama.Options{Host: tc.host})
		if c != nil || !errors.Is(err, ollama.ErrInvalidHost) {
			t.Errorf("New(%q) = %v, %v; want ErrInvalidHost", tc.host, c, err)
			continue
		}
		if msg := err.Error(); !strings.HasPrefix(msg, "ollama:") || !strings.Contains(msg, tc.why) || strings.Contains(msg, "s3cretpw") {
			t.Errorf("New(%q) error %q: want an ollama: error saying %q without the password", tc.host, msg, tc.why)
		}
	}
}
