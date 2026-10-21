package ollama_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// getenvSpy is an Options.Getenv that records every name it is asked for and
// answers with value.
type getenvSpy struct {
	value string
	keys  []string
}

func (s *getenvSpy) get(name string) string {
	s.keys = append(s.keys, name)
	return s.value
}

// TS-28-1: New with a valid model and host returns a ProviderClient, makes no
// request and exposes Host and API.
func TestNewMakesNoRequest_TS28_1(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)

	c, err := ollama.New(core.Model{ID: "qwen3.8:27b"}, ollama.Options{Env: map[string]string{ollama.HostVar: srv.URL}})
	if err != nil || c == nil {
		t.Fatalf("New = %v, %v; want a client", c, err)
	}
	var _ core.ProviderClient = c
	if c.Host() != srv.URL {
		t.Errorf("Host() = %q, want the server URL %q", c.Host(), srv.URL)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server saw %d requests after New, want 0", n)
	}
	if ollama.API != core.APIOllamaChat {
		t.Errorf("API = %q, want %q", ollama.API, core.APIOllamaChat)
	}
}

// TS-28-2: New refuses an empty or whitespace-only model id.
func TestNewRefusesAnEmptyModelID_TS28_2(t *testing.T) {
	for _, id := range []string{"", "   \t", "\n"} {
		c, err := ollama.New(core.Model{ID: id}, ollama.Options{Host: "h"})
		if c != nil || err == nil {
			t.Fatalf("New(%q) = %v, %v; want no client and an error", id, c, err)
		}
		if got := err.Error(); len(got) < 7 || got[:7] != "ollama:" {
			t.Errorf("New(%q) error %q does not start with \"ollama:\"", id, got)
		}
	}
}

// TS-28-3: the host comes from Options.Host, then Options.Env (an empty value
// falls through), then Options.Getenv, then the process environment, then the
// default; and the only variable ever asked for is OLLAMA_HOST.
func TestHostLookupOrder_TS28_3(t *testing.T) {
	m := core.Model{ID: "m"}
	t.Setenv(ollama.HostVar, "process.example")

	tests := []struct {
		name     string
		opts     func(*getenvSpy) ollama.Options
		spyValue string
		want     string
		wantKeys []string
	}{
		{"Host wins and reads nothing", func(s *getenvSpy) ollama.Options {
			return ollama.Options{Host: "h1", Env: map[string]string{ollama.HostVar: "e1"}, Getenv: s.get}
		}, "g1", "http://h1:11434", nil},
		{"an empty Env value falls through to Getenv", func(s *getenvSpy) ollama.Options {
			return ollama.Options{Env: map[string]string{ollama.HostVar: ""}, Getenv: s.get}
		}, "g1", "http://g1:11434", []string{ollama.HostVar}},
		{"a non-empty Env value wins over Getenv", func(s *getenvSpy) ollama.Options {
			return ollama.Options{Env: map[string]string{ollama.HostVar: "e1"}, Getenv: s.get}
		}, "g1", "http://e1:11434", nil},
		{"nothing set gives the default", func(s *getenvSpy) ollama.Options {
			return ollama.Options{Getenv: s.get}
		}, "", ollama.DefaultHost, []string{ollama.HostVar}},
		{"a Getenv hides the process environment", func(s *getenvSpy) ollama.Options {
			return ollama.Options{Getenv: s.get}
		}, "", ollama.DefaultHost, []string{ollama.HostVar}},
		{"the process environment is used when Getenv is nil", func(*getenvSpy) ollama.Options {
			return ollama.Options{}
		}, "", "http://process.example:11434", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spy := &getenvSpy{value: tc.spyValue}
			c, err := ollama.New(m, tc.opts(spy))
			if err != nil {
				t.Fatal(err)
			}
			if c.Host() != tc.want {
				t.Errorf("Host() = %q, want %q", c.Host(), tc.want)
			}
			if len(spy.keys) != len(tc.wantKeys) {
				t.Fatalf("Getenv was asked for %v, want %v", spy.keys, tc.wantKeys)
			}
			for i, k := range spy.keys {
				if k != tc.wantKeys[i] || k != ollama.HostVar {
					t.Errorf("Getenv was asked for %q, want only %q", k, ollama.HostVar)
				}
			}
		})
	}
}
