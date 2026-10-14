package gateway

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-19-15: Reap returns the server's ids sorted and surfaces the server's
// error text. 19-REQ-3.2.
func TestReap_TS19_15(t *testing.T) {
	f := newFakeGW(t, nil)
	f.set(func() {
		f.reaps = []*sandboxpb.ReapResponse{{Ids: []string{"c", "a", "b"}}, {Ids: []string{"b", "a"}, Error: "remove x: busy"}}
	})
	p := f.dial(t)
	ids, err := p.Reap(context.Background())
	if err != nil || !slices.Equal(ids, []string{"a", "b", "c"}) {
		t.Fatalf("first Reap = %v, %v; want [a b c], nil", ids, err)
	}
	ids, err = p.Reap(context.Background())
	if !slices.Equal(ids, []string{"a", "b"}) || err == nil || !strings.Contains(err.Error(), "remove x: busy") {
		t.Fatalf("second Reap = %v, %v; want [a b] and the server's error text", ids, err)
	}
}

// TS-19-16: List returns the caller's sandboxes as Info sorted by creation
// time and then id. 19-REQ-3.3.
func TestList_TS19_16(t *testing.T) {
	t1, t2 := time.Now().Add(-time.Hour).UnixNano(), time.Now().UnixNano()
	e2 := t2 + int64(time.Hour)
	f := newFakeGW(t, nil)
	f.set(func() {
		f.list = &sandboxpb.ListResponse{Sandboxes: []*sandboxpb.SandboxEntry{
			{Id: "z", CreatedUnixNanos: t1, ExpiresUnixNanos: t1 + 1},
			{Id: "b", Labels: map[string]string{"k": "w"}, CreatedUnixNanos: t2, ExpiresUnixNanos: e2},
			{Id: "a", Labels: map[string]string{"k": "v"}, CreatedUnixNanos: t2, ExpiresUnixNanos: e2},
		}}
	})
	infos, err := f.dial(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, i := range infos {
		ids = append(ids, i.ID)
	}
	if !slices.Equal(ids, []string{"z", "a", "b"}) {
		t.Fatalf("List ids %v, want [z a b]", ids)
	}
	a := infos[1]
	if a.Labels["k"] != "v" || len(a.Labels) != 1 || !a.Created.Equal(time.Unix(0, t2)) || !a.Expires.Equal(time.Unix(0, e2)) {
		t.Errorf("Info %+v does not carry the entry's labels and times", a)
	}
}
