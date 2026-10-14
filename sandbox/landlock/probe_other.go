//go:build !linux

package landlock

import "errors"

// defaultProbe has no Landlock to query off Linux; New refuses before it.
func defaultProbe() (int, error) { return 0, errors.ErrUnsupported }
