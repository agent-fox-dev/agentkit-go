package landlock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// markerName is the marker's file name in a sandbox directory.
const markerName = "agentkit-sandbox.json"

// marker names a sandbox's owner, first the creating process and then the
// agent, by pid, start time and boot id, so Reap can tell a live owner from
// a reused pid. It never holds Spec.Env.
type marker struct {
	PID       int               `json:"pid"`
	StartTime uint64            `json:"start_time"`
	BootID    string            `json:"boot_id"`
	Role      string            `json:"role"` // "creator" or "agent"
	Created   time.Time         `json:"created"`
	TTL       time.Duration     `json:"ttl"`
	Network   string            `json:"network"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// writeMarker writes m as dir's marker, mode 0600.
func writeMarker(dir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, markerName)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// replaceMarker replaces dir's marker with m atomically: a temporary file in
// dir, renamed over it.
func replaceMarker(dir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agentkit-sandbox-*.json")
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := firstErr(werr, cerr, os.Chmod(f.Name(), 0o600)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, markerName)); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// readMarker decodes dir's marker.
func readMarker(dir string) (marker, error) {
	var m marker
	b, err := os.ReadFile(filepath.Join(dir, markerName))
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

// firstErr is the first non-nil error.
func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
