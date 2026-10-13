//go:build !unix

package local

// canProbe: no liveness probe here, so Reap removes nothing.
const canProbe = false

func alive(int) bool { return true }
