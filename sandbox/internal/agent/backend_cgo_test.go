//go:build cgo

package agent

// wantOutlineBackend is TS-13-15's expectation in a cgo build.
const wantOutlineBackend = "tree-sitter"
