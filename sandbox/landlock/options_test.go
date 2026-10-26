package landlock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Root is made absolute, resolved through symlinks, created 0700 when
// missing, and defaults to agentkit-sandbox-landlock in the user cache.
func TestResolveRootGivesAnAbsoluteResolvedDirectory(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	ucd, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	// On Darwin, UserCacheDir ignores XDG_CACHE_HOME and always resolves to
	// $HOME/Library/Caches, which a bare HOME from t.TempDir() does not have.
	if err := os.MkdirAll(ucd, 0o700); err != nil {
		t.Fatal(err)
	}
	base := evalSymlinks(t, t.TempDir())
	if err := os.Mkdir(filepath.Join(base, "rel"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "rel"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	for given, want := range map[string]string{
		"": filepath.Join(evalSymlinks(t, ucd), "agentkit-sandbox-landlock"), "rel": filepath.Join(base, "rel"),
		filepath.Join(base, "link"): filepath.Join(base, "rel"), filepath.Join(base, "a", "b"): filepath.Join(base, "a", "b"),
	} {
		got, err := resolveRoot(given)
		if fi, serr := os.Stat(want); err != nil || got != want || serr != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("resolveRoot(%q) = %q, %v; want %s, mode 0700 (%v)", given, got, err, want, serr)
		}
	}
	for _, bad := range []string{filepath.Join(base, "rel", "f"), filepath.Join(base, "rel", "f", "sub")} {
		_ = os.WriteFile(filepath.Join(base, "rel", "f"), nil, 0o644)
		if _, err := resolveRoot(bad); err == nil || !strings.Contains(err.Error(), `landlock: Options.Root "`+bad+`"`) {
			t.Errorf("resolveRoot(%s) = %v, want an error naming Root", bad, err)
		}
	}
}

// An entry must be absolute, not /, and not equal, contain or lie beneath Root.
func TestCheckExtraRefusesWhatWouldReachRoot(t *testing.T) {
	root := evalSymlinks(t, t.TempDir())
	other := t.TempDir()
	link := filepath.Join(other, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		paths []string
		want  string // what the refusal says; "" when accepted
	}{
		{[]string{other, "rel/dir"}, `Options.ExtraRO[1] "rel/dir" must be an absolute directory other than /`},
		{[]string{"/"}, `Options.ExtraRO[0] "/" must be an absolute`},
		{[]string{root}, "overlaps Options.Root " + root},
		{[]string{filepath.Dir(root)}, "overlaps Options.Root"},
		{[]string{filepath.Join(root, "missing", "sub")}, "overlaps Options.Root"},
		{[]string{link}, "overlaps Options.Root"},
		{[]string{root + "/../" + filepath.Base(root) + "/x"}, "overlaps Options.Root"},
		{[]string{root + "-other", other + "/missing/../m"}, ""},
	} {
		got, err := checkExtra("ExtraRO", c.paths, root)
		switch {
		case c.want == "" && (err != nil || got[0] != root+"-other" || got[1] != other+"/m"):
			t.Errorf("%q: %q, %v", c.paths, got, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || errors.Is(err, ErrNoLandlock)):
			t.Errorf("%q: %v, want an error containing %q", c.paths, err, c.want)
		}
	}
}
