//go:build cgo

package agent

// outlineBackend is the outline backend for non-Go files in a cgo build.
const outlineBackend = "tree-sitter"
