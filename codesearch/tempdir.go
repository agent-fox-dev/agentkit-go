//go:build !windows

package codesearch

import "os"

func defaultTempDir() string {
	return os.TempDir()
}
