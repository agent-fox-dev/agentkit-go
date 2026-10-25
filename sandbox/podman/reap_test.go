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
	raw    string            // when set, what inspect prints instead
	rmErr  string            // when set, its removal fails with this output
}

var reapT = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func labels(id string, exp time.Time) map[string]string {
	return map[string]string{labelID: id, labelExpires: exp.Format(time.RFC3339)}
}

// reaper is a provider at reapT over a fake engine listing these entries, with stderr noise.
func reaper(f *fakeRunner, containers, volumes []engineEntry) *provider {
	f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		list, verb := containers, argv[1]
		if verb == "volume" {
			list, verb = volumes, argv[2]
		}
		var e engineEntry
		if i := slices.IndexFunc(list, func(e engineEntry) bool { return e.name == argv[len(argv)-1] }); i >= 0 {
			e = list[i]
		}
		res := exitResult(0, e.name+"\n")
		switch b, _ := json.Marshal(e.labels); {
		case verb == "ps" || verb == "ls":
			var names []string
			for _, e := range list {
				names = append(names, e.name)
			}
			res = exitResult(0, strings.Join(names, "\n"))
		case verb == "inspect" && e.raw != "":
			res = exitResult(0, e.raw)
		case verb == "inspect" && e.labels == nil:
			res = exitResult(125, "Error: inspecting "+e.name)
		case verb == "inspect":
			res = exitResult(0, string(b)+"\n")
		case e.rmErr != "":
			res = exitResult(1, e.rmErr)
		}
		res.Output = "WARN[0000] rootless: cgroup v1\n" + res.Output
		return res, nil
	}
	p := testProvider(f, Options{})
	p.now = func() time.Time { return reapT }
	return p
}

// Unexpired, unlabelled and unreadable entries are spared; failures are joined.
func TestReapRemovesOnlyExpiredEntriesContainersFirst(t *testing.T) {
	f := newFake(t)
	past := labels("aaaa", reapT.Add(-time.Hour))
	ids, err := reaper(f, []engineEntry{{name: "agentkit-aaaa", labels: past}}, []engineEntry{
		{name: "agentkit-aaaa", labels: past}, {name: "agentkit-bbbb", labels: labels("bbbb", reapT)},
		{name: "agentkit-cccc", labels: labels("cccc", reapT.Add(time.Hour))},
		{name: "agentkit-dddd", labels: map[string]string{labelID: "dddd"}},
		{name: "agentkit-eeee", labels: map[string]string{labelID: "eeee", labelExpires: "yesterday"}},
		{name: "agentkit-ffff"}, {name: "agentkit-gggg", raw: "not json"},
		{name: "agentkit-hhhh", labels: labels("hhhh", reapT.Add(-time.Hour)), rmErr: "Error: device busy"},
	}).Reap(context.Background())
	if !slices.Equal(ids, []string{"aaaa", "bbbb"}) || err == nil || !strings.Contains(err.Error(), "device busy") {
		t.Fatalf("Reap = %v, %v; want [aaaa bbbb] and the device busy error", ids, err)
	}
	if !f.ran("ps", "-a", "--filter", "label=agentkit.sandbox", "--format", "{{.Names}}") ||
		!f.ran("volume", "ls", "--filter", "label=agentkit.sandbox", "--format", "{{.Name}}") ||
		!f.ran("inspect", "--format", "{{json .Config.Labels}}", "agentkit-aaaa") ||
		!f.ran("volume", "inspect", "--format", "{{json .Labels}}", "agentkit-bbbb") {
		t.Errorf("Reap did not list and inspect by the label: %v", f.recorded())
	}
	for _, id := range []string{"cccc", "dddd", "eeee", "ffff", "gggg"} {
		if f.ran("volume", "rm", "agentkit-"+id) {
			t.Errorf("Reap removed %s", id)
		}
	}
	if rc, va := f.index(0, "rm", "-f", "agentkit-aaaa"), f.index(0, "volume", "rm"); rc < 0 || va < rc || !f.ran("volume", "rm", "agentkit-hhhh") {
		t.Errorf("removals out of order or missing: %v", f.recorded())
	}
}

func TestReapFailsWhenAListingFails(t *testing.T) {
	for _, failing := range [][]string{{"ps"}, {"volume", "ls"}} {
		f := newFake(t)
		f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
			return exitResult(0, ""), map[bool]error{true: errors.New("engine gone")}[hasArgs(argv, failing...)]
		}
		if ids, err := testProvider(f, Options{}).Reap(context.Background()); ids != nil || err == nil || !strings.Contains(err.Error(), "podman "+strings.Join(failing, " ")) {
			t.Errorf("%s fails: Reap = %v, %v", failing, ids, err)
		}
	}
}
