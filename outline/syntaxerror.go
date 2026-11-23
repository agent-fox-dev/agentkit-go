package outline

import (
	"cmp"
	"slices"
)

// defaultMaxSyntaxErrors is how many syntax errors a File holds when
// Options.MaxSyntaxErrors is zero or negative.
const defaultMaxSyntaxErrors = 20

// limitSyntaxErrors orders errs by line, then column, and keeps the first
// max of them (defaultMaxSyntaxErrors when max is not positive). It reports
// whether any were cut, and never returns nil.
func limitSyntaxErrors(errs []SyntaxError, max int) ([]SyntaxError, bool) {
	if max <= 0 {
		max = defaultMaxSyntaxErrors
	}
	out := slices.Clone(errs)
	if out == nil {
		out = []SyntaxError{}
	}
	slices.SortStableFunc(out, func(a, b SyntaxError) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})
	if len(out) > max {
		return out[:max], true
	}
	return out, false
}
