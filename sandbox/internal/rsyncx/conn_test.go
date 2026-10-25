package rsyncx

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func TestConnWriteSendsDataFramesOfAtMostOneMiB(t *testing.T) {
	big := bytes.Repeat([]byte{7}, 2<<20+1<<19+1)
	for name, mk := range map[string]func(*fakeStream) *Conn{
		"host":  func(f *fakeStream) *Conn { return HostConn(f) },
		"agent": func(f *fakeStream) *Conn { return AgentConn(f) },
	} {
		f := &fakeStream{}
		c := mk(f)
		if n, err := c.Write(big); n != len(big) || err != nil {
			t.Fatalf("%s: Write = %d, %v", name, n, err)
		}
		var sizes []int
		var got []byte
		for _, d := range f.sentData() {
			sizes = append(sizes, len(d))
			got = append(got, d...)
		}
		if len(sizes) != 3 || sizes[0] != 1<<20 || sizes[1] != 1<<20 || sizes[2] != 1<<19+1 || !bytes.Equal(got, big) {
			t.Errorf("%s: frame sizes %v, want [1<<20 1<<20 1<<19+1] concatenating to the input", name, sizes)
		}
		if c.Sent() != int64(len(big)) {
			t.Errorf("%s: Sent() = %d, want %d", name, c.Sent(), len(big))
		}
	}
}

func TestConnDoesNotInterleaveConcurrentWrites(t *testing.T) {
	f := &fakeStream{}
	c := HostConn(f)
	const run = 1<<20 + 1<<19
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, err := c.Write(bytes.Repeat([]byte{byte(i + 1)}, run)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var all []byte
	for _, d := range f.sentData() {
		all = append(all, d...)
	}
	if len(all) != 8*run {
		t.Fatalf("%d bytes sent, want %d", len(all), 8*run)
	}
	seen := map[byte]bool{}
	for i := 0; i < len(all); i += run {
		b := all[i]
		if seen[b] || !bytes.Equal(all[i:i+run], bytes.Repeat([]byte{b}, run)) {
			t.Fatalf("run %d is not one contiguous write of byte %d: writes interleaved", i/run, b)
		}
		seen[b] = true
	}
}

func TestConnWriteFailsAfterCloseOrWithTheStream(t *testing.T) {
	c := AgentConn(&fakeStream{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("x")); err == nil {
		t.Error("a Write after Close succeeded")
	}
	boom := errors.New("boom")
	if n, err := AgentConn(&fakeStream{sendErr: boom}).Write([]byte("x")); n != 0 || !errors.Is(err, boom) {
		t.Errorf("Write on a failing stream = %d, %v", n, err)
	}
}

// On the host a done frame ends reads with io.EOF and is kept; Close
// half-closes the send side once.
func TestHostConnReadsDataUntilTheDoneFrame(t *testing.T) {
	f := &fakeStream{recv: []*sandboxpb.RsyncFrame{dataFrame("ab"), dataFrame("cd"), doneFrame(2)}}
	c := HostConn(f)
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "abcd" {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	if n, err := c.Read(make([]byte, 4)); n != 0 || err != io.EOF {
		t.Errorf("Read after done = %d, %v; want 0, io.EOF", n, err)
	}
	if d, ok := c.Done(); !ok || d.Files != 2 {
		t.Errorf("Done() = %v, %v", d, ok)
	}
	if c.Ended() || c.Failure() != nil {
		t.Errorf("Ended() = %v, Failure() = %v after a done frame", c.Ended(), c.Failure())
	}
	_ = c.Close()
	_ = c.Close()
	if f.closeSends != 1 {
		t.Errorf("CloseSend called %d times, want 1", f.closeSends)
	}
}

func TestHostConnEndedReportsAStreamThatEndedWithoutDone(t *testing.T) {
	c := HostConn(&fakeStream{recv: []*sandboxpb.RsyncFrame{dataFrame("x")}})
	if _, err := io.ReadAll(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Done(); ok || !c.Ended() {
		t.Errorf("Done ok = %v, Ended() = %v; want no done frame and Ended", ok, c.Ended())
	}
}

func TestConnReadRecordsAFrameOutOfOrderOrAStreamError(t *testing.T) {
	boom := errors.New("reset")
	for _, c := range []struct {
		name string
		conn *Conn
		kind string
		err  error
	}{
		{"agent done", AgentConn(&fakeStream{recv: []*sandboxpb.RsyncFrame{doneFrame(0)}}), "done", nil},
		{"agent args", AgentConn(&fakeStream{recv: []*sandboxpb.RsyncFrame{{Frame: &sandboxpb.RsyncFrame_Args{}}}}), "args", nil},
		{"host plan", HostConn(&fakeStream{recv: []*sandboxpb.RsyncFrame{{Frame: &sandboxpb.RsyncFrame_Plan{}}}}), "plan", nil},
		{"host empty", HostConn(&fakeStream{recv: []*sandboxpb.RsyncFrame{{}}}), "empty", nil},
		{"stream error", HostConn(&fakeStream{end: boom}), "", boom},
	} {
		_, err := c.conn.Read(make([]byte, 1))
		var fe *FrameError
		switch {
		case c.err != nil && (!errors.Is(err, c.err) || !errors.Is(c.conn.Failure(), c.err)):
			t.Errorf("%s: Read = %v, Failure = %v; want %v", c.name, err, c.conn.Failure(), c.err)
		case c.err == nil && (!errors.As(err, &fe) || fe.Kind != c.kind || c.conn.Failure() != err):
			t.Errorf("%s: Read = %v, Failure = %v; want a FrameError of kind %s", c.name, err, c.conn.Failure(), c.kind)
		}
	}
	if got := (&FrameError{Kind: "plan"}).Error(); got != "unexpected plan frame" {
		t.Errorf("FrameError = %q", got)
	}
}

func TestFrameKindNamesEachFrame(t *testing.T) {
	for want, f := range map[string]*sandboxpb.RsyncFrame{
		"start": {Frame: &sandboxpb.RsyncFrame_Start{}}, "plan": {Frame: &sandboxpb.RsyncFrame_Plan{}},
		"args": {Frame: &sandboxpb.RsyncFrame_Args{}}, "data": dataFrame(""), "done": doneFrame(0), "empty": {},
	} {
		if got := FrameKind(f); got != want {
			t.Errorf("FrameKind = %q, want %q", got, want)
		}
	}
}
