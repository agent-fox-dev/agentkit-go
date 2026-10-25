package tarsafe

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func names(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h.Name)
	}
}

func TestValidateStreamPassesAcceptedEntriesSanitised(t *testing.T) {
	var out bytes.Buffer
	if err := ValidateStream(bytes.NewReader(tarOf(t, folder("d/"), reg("d/f", "body", 0o6755), symlink("l", "d"))), &out); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		got = append(got, h.Name+":"+string(body))
		if h.Mode&0o7000 != 0 {
			t.Errorf("%s keeps mode %o", h.Name, h.Mode)
		}
	}
	if strings.Join(got, ",") != "d/:,d/f:body,l:" {
		t.Errorf("validated stream holds %v", got)
	}
}

func TestValidateStreamStopsBeforeTheFirstRefusedEntry(t *testing.T) {
	var out bytes.Buffer
	err := ValidateStream(bytes.NewReader(tarOf(t, reg("ok", "x", 0o644), symlink("bad", "/etc"), reg("after", "", 0o644))), &out)
	var ee *EntryError
	if !errors.As(err, &ee) || ee.Name != "bad" {
		t.Fatalf("ValidateStream = %v, want an EntryError naming bad", err)
	}
	if got := names(t, append(out.Bytes(), make([]byte, 1024)...)); len(got) != 1 || got[0] != "ok" {
		t.Errorf("the output holds %v, want only ok", got)
	}
}

func TestValidateStreamReportsAMalformedStreamAndASourceFailure(t *testing.T) {
	blob := tarOf(t, reg("a", strings.Repeat("x", 2000), 0o644))
	var ee *EntryError
	if err := ValidateStream(bytes.NewReader(blob[:1000]), io.Discard); !errors.As(err, &ee) || ee.Name != "a" {
		t.Errorf("a cut stream = %v, want an EntryError naming a", err)
	}
	if err := ValidateStream(strings.NewReader(strings.Repeat("\x01", 512)), io.Discard); !errors.As(err, &ee) || ee.Name != "" {
		t.Errorf("garbage = %v, want an EntryError naming no entry", err)
	}
	boom := errors.New("boom")
	if err := ValidateStream(&failingReader{blob[:1000], boom}, io.Discard); !errors.Is(err, boom) {
		t.Errorf("a failing source = %v, want its own error", err)
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func TestValidateStreamReturnsAWriteErrorAsItIs(t *testing.T) {
	boom := errors.New("full")
	err := ValidateStream(bytes.NewReader(tarOf(t, reg("a", strings.Repeat("x", 4096), 0o644))), failingWriter{boom})
	if !errors.Is(err, boom) {
		t.Errorf("ValidateStream = %v, want the writer's error", err)
	}
}
