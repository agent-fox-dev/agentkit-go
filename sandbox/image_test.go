package sandbox_test

import (
	"fmt"
	"math/rand/v2"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-16-4: ImageTag accepts canonical release and prerelease versions and
// strips the leading v. 16-REQ-5.2.
func TestImageTagAccepts_TS16_4(t *testing.T) {
	for _, v := range []string{"v0.6.0", "v1.2.3-rc.1", "v10.20.30", "v0.0.0", "v1.0.0-alpha", "v1.2.3-rc.20240102150405"} {
		if tag, ok := sandbox.ImageTag(v); !ok || tag != v[1:] {
			t.Errorf("ImageTag(%q) = (%q, %v), want (%q, true)", v, tag, ok, v[1:])
		}
	}
}

// TS-16-5: ImageTag refuses non-canonical versions, build metadata and all
// three pseudo-version forms with ("", false). 16-REQ-5.3, 16-REQ-5.4.
func TestImageTagRefuses_TS16_5(t *testing.T) {
	for _, v := range []string{"", "(devel)", "v1.2.3+dirty", "v2.0.0+incompatible", "v1.2.3-rc.1+build.5", "1.2.3", "V1.2.3",
		"v01.2.3", "v1.02.3", "v1.2.03", "v1.2", "v1", "v1.2.3-", "v1.2.3.4", " v1.2.3", "latest",
		"v0.0.0-20240102150405-abcdefabcdef", "v1.2.4-0.20240102150405-abcdefabcdef", "v1.2.3-pre.0.20240102150405-abcdefabcdef",
		"v1.2.3-pre.20240102150405-abcdefabcdef", "v0.0.0-20240102150405-abcdefabcdef+dirty"} {
		if tag, ok := sandbox.ImageTag(v); ok || tag != "" {
			t.Errorf("ImageTag(%q) = (%q, %v), want (\"\", false)", v, tag, ok)
		}
	}
}

// pseudo is the independent pseudo-version oracle over a prerelease.
var pseudo = regexp.MustCompile(`(^|\.)[0-9]{14}-[0-9a-f]{12}$`)

// TS-16-6: whatever ImageTag accepts is v plus its tag, carries no build
// metadata and is not a pseudo-version. 16-REQ-5.8.
func TestImageTagProperty_TS16_6(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	part := func() string {
		s := fmt.Sprint(r.IntN(30))
		if r.IntN(6) == 0 {
			s = "0" + s
		}
		return s
	}
	const chars = "abcXYZ019.-"
	for range 5000 {
		var b strings.Builder
		b.WriteString([]string{"v", "V", ""}[r.IntN(3)])
		n := 1 + r.IntN(4)
		for i := range n {
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(part())
		}
		if r.IntN(2) == 0 {
			b.WriteByte('-')
			for range r.IntN(4) {
				if r.IntN(4) == 0 {
					b.WriteString(fmt.Sprintf("%014d-%012x", r.Int64N(1e14), r.Int64N(1<<48)))
				} else {
					b.WriteByte(chars[r.IntN(len(chars))])
				}
			}
		}
		if r.IntN(5) == 0 {
			b.WriteString("+meta")
		}
		if r.IntN(20) == 0 {
			b.WriteByte(byte(r.IntN(256)))
		}
		v := b.String()
		tag, ok := sandbox.ImageTag(v)
		if tag2, ok2 := sandbox.ImageTag(v); tag2 != tag || ok2 != ok {
			t.Fatalf("ImageTag(%q) is not deterministic", v)
		}
		if !ok {
			if tag != "" {
				t.Fatalf("ImageTag(%q) refused but returned %q", v, tag)
			}
			continue
		}
		if tag == "" || "v"+tag != v || strings.Contains(v, "+") {
			t.Fatalf("ImageTag(%q) = %q", v, tag)
		}
		if _, pre, found := strings.Cut(tag, "-"); found && pseudo.MatchString(pre) {
			t.Fatalf("ImageTag accepted the pseudo-version %q", v)
		}
	}
}

// TS-16-9: OverlayCopyLine returns the single COPY --from line that installs
// the agent at AgentPath. 16-REQ-5.7.
func TestOverlayCopyLine_TS16_9(t *testing.T) {
	if got := sandbox.OverlayCopyLine("x:1"); got != "COPY --from=x:1 /usr/local/libexec/agentkit-sandboxd /usr/local/libexec/" {
		t.Errorf("OverlayCopyLine(x:1) = %q", got)
	}
	ref := "quay.io/agent-fox-dev/agentkit-sandboxd:0.6.0"
	got := sandbox.OverlayCopyLine(ref)
	if want := "COPY --from=" + ref + " " + sandbox.AgentPath + " " + path.Dir(sandbox.AgentPath) + "/"; got != want || strings.Contains(got, "\n") {
		t.Errorf("OverlayCopyLine(%s) = %q, want %q", ref, got, want)
	}
}
