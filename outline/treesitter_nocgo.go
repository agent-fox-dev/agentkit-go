//go:build !cgo

package outline

import "context"

// outlineTreeSitter needs cgo: without it no language other than Go has an
// outline backend.
func outlineTreeSitter(context.Context, string, string, []byte) ([]Decl, []SyntaxError, bool, error) {
	return nil, nil, false, nil
}

// commentAndStringSpans needs cgo: without it no grammar is known.
func commentAndStringSpans(context.Context, string, []byte) ([][2]int, bool) {
	return nil, false
}
