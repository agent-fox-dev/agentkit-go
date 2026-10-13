package rsyncx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// fakeStream records sent frames and replays scripted received ones.
type fakeStream struct {
	mu         sync.Mutex
	sent       []*sandboxpb.RsyncFrame
	recv       []*sandboxpb.RsyncFrame
	closeSends int
}

func (f *fakeStream) Send(fr *sandboxpb.RsyncFrame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fr)
	return nil
}

func (f *fakeStream) Recv() (*sandboxpb.RsyncFrame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.recv) == 0 {
		return nil, io.EOF
	}
	fr := f.recv[0]
	f.recv = f.recv[1:]
	return fr, nil
}

func (f *fakeStream) CloseSend() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeSends++
	return nil
}

func (f *fakeStream) Context() context.Context { return context.Background() }

func dataFrame(b string) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: []byte(b)}}
}

// sentData is the data frames' payloads, in order.
func (f *fakeStream) sentData() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]byte
	for _, fr := range f.sent {
		out = append(out, fr.GetData())
	}
	return out
}

// TS-14-8: the stream adapter chunks writes to at most 1 MiB, serialises
// concurrent writes, half-closes on Close and returns io.EOF after done.
// 14-REQ-2.5, 14-REQ-2.6.
func TestStreamAdapter_TS14_8(t *testing.T) {
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
		if len(d) > 1<<20 {
			t.Fatalf("a data frame of %d bytes", len(d))
		}
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

	f = &fakeStream{recv: []*sandboxpb.RsyncFrame{dataFrame("ab"), dataFrame("cd"),
		{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{Files: 2}}}}}
	c = HostConn(f)
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "abcd" {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
	buf := make([]byte, 4)
	for range 2 {
		if n, err := c.Read(buf); n != 0 || err != io.EOF {
			t.Errorf("Read after done = %d, %v; want 0, io.EOF", n, err)
		}
	}
	if d, ok := c.Done(); !ok || d.Files != 2 {
		t.Errorf("Done() = %v, %v", d, ok)
	}
	_ = c.Close()
	_ = c.Close()
	if f.closeSends != 1 {
		t.Errorf("CloseSend called %d times, want 1", f.closeSends)
	}

	// The agent end treats a done frame, and any non-data frame, as out of
	// order.
	f = &fakeStream{recv: []*sandboxpb.RsyncFrame{dataFrame("x"), {Frame: &sandboxpb.RsyncFrame_Args{Args: &sandboxpb.RsyncArgs{}}}}}
	a := AgentConn(f)
	if n, err := a.Read(buf); n != 1 || err != nil {
		t.Fatalf("agent Read = %d, %v", n, err)
	}
	var fe *FrameError
	if _, err := a.Read(buf); !errors.As(err, &fe) || fe.Kind != "args" {
		t.Errorf("agent Read of an args frame = %v, want a FrameError naming args", err)
	}
}
