package podman

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// engineEntry is a labelled container or volume the fake engine lists.
type engineEntry struct {
	name   string
	labels map[string]string // nil: inspect fails
	rmErr  string            // non-empty: its removal fails with this output
}

// listingAnswer serves containers and volumes to Reap's listings, inspect
// commands and removals.
func listingAnswer(containers, volumes []engineEntry) func(context.Context, []string) (tools.ExecResult, error) {
	find := func(list []engineEntry, name string) (engineEntry, bool) {
		for _, e := range list {
			if e.name == name {
				return e, true
			}
		}
		return engineEntry{}, false
	}
	names := func(list []engineEntry) string {
		var b strings.Builder
		for _, e := range list {
			b.WriteString(e.name + "\n")
		}
		return b.String()
	}
	inspect := func(list []engineEntry, name string) (tools.ExecResult, error) {
		e, ok := find(list, name)
		if !ok || e.labels == nil {
			return exitResult(125, "Error: inspecting "+name+": failed"), nil
		}
		b, _ := json.Marshal(e.labels)
		return exitResult(0, string(b)+"\n"), nil
	}
	remove := func(list []engineEntry, name string) (tools.ExecResult, error) {
		if e, ok := find(list, name); ok && e.rmErr != "" {
			return exitResult(1, e.rmErr), nil
		}
		return exitResult(0, name+"\n"), nil
	}
	return func(_ context.Context, argv []string) (tools.ExecResult, error) {
		last := argv[len(argv)-1]
		switch {
		case hasArgs(argv, "ps", "-a"):
			return exitResult(0, names(containers)), nil
		case hasArgs(argv, "volume", "ls"):
			return exitResult(0, names(volumes)), nil
		case hasArgs(argv, "volume", "inspect"):
			return inspect(volumes, last)
		case hasArgs(argv, "inspect"):
			return inspect(containers, last)
		case hasArgs(argv, "volume", "rm"):
			return remove(volumes, last)
		case hasArgs(argv, "rm", "-f"):
			return remove(containers, last)
		}
		return exitResult(0, ""), nil
	}
}

// labels is an entry's labels with the given id and expiry.
func labels(id, expires string) map[string]string {
	m := map[string]string{"agentkit.sandbox": id}
	if expires != "" {
		m["agentkit.sandbox.expires"] = expires
	}
	return m
}

var reapT = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

// rfc is t in the label format.
func rfc(t time.Time) string { return t.Format(time.RFC3339) }

// TS-17-50: Reap lists by label key, reads labels from inspect JSON and
// removes expired entries, containers first. 17-REQ-8.1, 17-REQ-8.2.
func TestReapRemovesExpired_TS17_50(t *testing.T) {
	f := newFake(t)
	f.answer = listingAnswer(
		[]engineEntry{{name: "agentkit-aaaa", labels: labels("aaaa", rfc(reapT.Add(-time.Hour)))}},
		[]engineEntry{{name: "agentkit-aaaa", labels: labels("aaaa", rfc(reapT.Add(-time.Hour)))}, {name: "agentkit-bbbb", labels: labels("bbbb", rfc(reapT))}})
	p := testProvider(f, Options{})
	p.now = func() time.Time { return reapT }
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{"aaaa", "bbbb"}) {
		t.Fatalf("Reap = %v, %v; want [aaaa bbbb], nil", ids, err)
	}
	if !f.ran("ps", "-a", "--filter", "label=agentkit.sandbox") || !f.ran("volume", "ls", "--filter", "label=agentkit.sandbox") {
		t.Errorf("Reap did not list by the label key: %v", f.recorded())
	}
	if !f.ran("inspect", "--format", "{{json .Config.Labels}}", "agentkit-aaaa") || !f.ran("volume", "inspect", "--format", "{{json .Labels}}", "agentkit-bbbb") {
		t.Errorf("Reap did not inspect the labels: %v", f.recorded())
	}
	rc := f.index(0, "rm", "-f", "agentkit-aaaa")
	va, vb := f.index(0, "volume", "rm", "agentkit-aaaa"), f.index(0, "volume", "rm", "agentkit-bbbb")
	if rc < 0 || va < 0 || vb < 0 || rc > va || rc > vb {
		t.Errorf("removals out of order or missing: rm %d, volume rm %d and %d", rc, va, vb)
	}
}

// TS-17-51: Reap leaves unexpired, unlabelled and unreadable entries alone
// and returns partial results with joined errors. 17-REQ-8.3, 17-REQ-8.4.
func TestReapSparesAndJoins_TS17_51(t *testing.T) {
	f := newFake(t)
	past := rfc(reapT.Add(-time.Hour))
	f.answer = listingAnswer(nil, []engineEntry{
		{name: "agentkit-cccc", labels: labels("cccc", rfc(reapT.Add(time.Hour)))},
		{name: "agentkit-dddd", labels: labels("dddd", "")},
		{name: "agentkit-eeee", labels: labels("eeee", "yesterday")},
		{name: "agentkit-ffff"},
		{name: "agentkit-gggg", labels: labels("gggg", past)},
		{name: "agentkit-hhhh", labels: labels("hhhh", past), rmErr: "Error: device busy"},
	})
	p := testProvider(f, Options{})
	p.now = func() time.Time { return reapT }
	ids, err := p.Reap(context.Background())
	if !slices.Equal(ids, []string{"gggg"}) || err == nil || !strings.Contains(err.Error(), "device busy") {
		t.Fatalf("Reap = %v, %v; want [gggg] and an error with device busy", ids, err)
	}
	for _, id := range []string{"cccc", "dddd", "eeee", "ffff"} {
		if f.ran("volume", "rm", "agentkit-"+id) || f.ran("rm", "-f", "agentkit-"+id) {
			t.Errorf("Reap removed %s", id)
		}
	}
	if !f.ran("volume", "rm", "agentkit-gggg") || !f.ran("volume", "rm", "agentkit-hhhh") {
		t.Error("Reap did not attempt both expired volumes")
	}
	if errors.Is(err, context.Canceled) {
		t.Error("unexpected cancellation")
	}
}
