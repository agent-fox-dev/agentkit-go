package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// markerFile names the marker in each sandbox directory.
const markerFile = "agentkit-sandbox.json"

// marker records who created a sandbox directory, so Reap can tell a
// directory whose creator has gone.
type marker struct {
	PID     int               `json:"pid"`
	Labels  map[string]string `json:"labels,omitempty"`
	Created time.Time         `json:"created"`
	TTLNs   int64             `json:"ttl_ns"`
}

func writeMarker(dir string, m marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, markerFile), b, 0o600)
}

func readMarker(dir string) (marker, error) {
	var m marker
	b, err := os.ReadFile(filepath.Join(dir, markerFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}
