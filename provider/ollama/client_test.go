package ollama_test

import (
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

func TestNewRefusesAModelIDThatIsBlank(t *testing.T) {
	for _, id := range []string{"", "   \t", "\n"} {
		if c, err := ollama.New(core.Model{ID: id}, ollama.Options{Host: "h"}); c != nil || err == nil || !strings.HasPrefix(err.Error(), "ollama:") {
			t.Errorf("New(%q) = %v, %v; want no client and an ollama: error", id, c, err)
		}
	}
}

// The host comes from Options.Host (reading no variable), then Options.Env (an
// empty value falls through), then Getenv, then the process environment, then
// the default; OLLAMA_HOST is the only variable asked for, and New makes no
// request.
func TestTheHostResolvesFromOptionsThenTheEnvironment(t *testing.T) {
	t.Setenv(ollama.HostVar, "process.example")
	srv := newServer(t)
	for _, tc := range []struct {
		name   string
		opts   ollama.Options
		getenv string // what Getenv answers; "-" for no Getenv
		want   string
		asked  int
	}{
		{"Host wins", ollama.Options{Host: "h1", Env: map[string]string{ollama.HostVar: "e1"}}, "g1", "http://h1:11434", 0},
		{"an empty Env value falls through", ollama.Options{Env: map[string]string{ollama.HostVar: ""}}, "g1", "http://g1:11434", 1},
		{"Env wins over Getenv", ollama.Options{Env: map[string]string{ollama.HostVar: srv.URL()}}, "g1", srv.URL(), 0},
		{"Getenv hides the process", ollama.Options{}, "", ollama.DefaultHost, 1},
		{"the process environment", ollama.Options{}, "-", "http://process.example:11434", 0},
	} {
		var asked []string
		if tc.getenv != "-" {
			tc.opts.Getenv = func(name string) string { asked = append(asked, name); return tc.getenv }
		}
		c, err := ollama.New(core.Model{ID: "m"}, tc.opts)
		if err != nil || c.Host() != tc.want {
			t.Errorf("%s: New = %v, %v; want Host %q", tc.name, c, err, tc.want)
		}
		if len(asked) != tc.asked || tc.asked == 1 && asked[0] != ollama.HostVar {
			t.Errorf("%s: Getenv was asked for %q", tc.name, asked)
		}
	}
	if srv.Count() != 0 {
		t.Errorf("New made %d requests", srv.Count())
	}
}
