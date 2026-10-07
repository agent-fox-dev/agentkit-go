package tools

import (
	"fmt"
	"sort"
	"strings"
)

// Edit is one requested replacement.
type Edit struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// EditError is a rejection with the exact model-visible text the PRD pins.
// The strings are part of the contract: two conforming implementations that
// worded them differently would show the model different messages and get
// different self-correction behaviour.
type EditError struct {
	Phase string
	Index int
	Text  string
}

func (e *EditError) Error() string { return e.Text }

// ApplyEdits applies a batch of edits to content, or rejects the whole batch.
//
// The contract, from REQ-TOOL-04a-d:
//
//   - Every old_string matches against the ORIGINAL content, never against the
//     result of an earlier edit in the same call. This is what makes a batch
//     reviewable: the model reasoned about one file, not about a moving one.
//   - A non-unique old_string is a REJECTION, not a replace-all (REQ-TOOL-04c).
//     Multiplicity means the model has not identified a site; replacing all of
//     them is how an agent corrupts a file it was asked to touch once. There
//     is deliberately no {replaced: N} success shape.
//   - Rejections are evaluated in GLOBAL PHASE ORDER, not per edit (ruling
//     P-22). Phases 4 and 5 are inherently global, and per-edit ordering is a
//     real behavioural fork: it would report "not found" for edit 2 while a
//     phase-ordered implementation reports "not unique" for edit 1, showing
//     the model a different problem to fix.
//
// Returns the new content and the number of edits applied.
func ApplyEdits(content string, edits []Edit) (string, int, error) {
	if len(edits) == 0 {
		return "", 0, &EditError{Phase: "empty", Text: "No edits were provided."}
	}
	// The edits get the same CRLF normalisation the file did (NormalizeForEdit).
	// A model that copied its old_string out of a CRLF file it read through a
	// path that kept the CRs sends "\r\n", the content it is matched against
	// has "\n", and nothing matches; Restore then puts the CRs back on the
	// new_string's lines along with everything else.
	edits = normalizeEdits(edits)
	// A rejection that names an edit by index only helps when there is more
	// than one to tell apart; for a single edit the prefix is noise.
	at := func(i int) string {
		if len(edits) > 1 {
			return fmt.Sprintf("edits[%d]: ", i)
		}
		return ""
	}

	// ---- Phase 1: empty old_string.
	for i, e := range edits {
		if e.OldString == "" {
			return "", 0, &EditError{Phase: "empty_old_string", Index: i,
				Text: fmt.Sprintf("edits[%d].old_string is empty. Provide the exact text to replace.", i)}
		}
	}

	// ---- Phase 2: not found.
	//
	// REQ-TOOL-04d's fallback hangs off this phase, and only this phase. An
	// edit that was FOUND exactly is never re-matched leniently: the fold
	// exists to rescue a batch that would otherwise be rejected outright, not
	// to widen matching for one that already works.
	for i, e := range edits {
		if !strings.Contains(content, e.OldString) {
			if out, n, ok := applyEditsFolded(content, edits); ok {
				return out, n, nil
			}
			return "", 0, &EditError{Phase: "not_found", Index: i,
				Text: at(i) + "The string to replace was not found in the file." + notFoundHint(content, e.OldString)}
		}
	}

	// ---- Phase 3: not unique. The pinned wording.
	for i, e := range edits {
		if n := countOverlapping(content, e.OldString); n > 1 {
			return "", 0, &EditError{Phase: "not_unique", Index: i,
				Text: at(i) + fmt.Sprintf("Found %d occurrences of the string to replace. "+
					"The text must be unique. Please provide more context to make it unique.", n)}
		}
	}

	// ---- Phase 4: overlap. Sort matches by offset and reject when the
	// previous match's end passes the next one's start. Two edits that overlap
	// cannot both be applied against the original, and applying either alone
	// silently drops the other.
	type match struct {
		idx    int
		offset int
		length int
	}
	ms := make([]match, 0, len(edits))
	for i, e := range edits {
		ms = append(ms, match{idx: i, offset: strings.Index(content, e.OldString), length: len(e.OldString)})
	}
	sort.Slice(ms, func(a, b int) bool { return ms[a].offset < ms[b].offset })
	for k := 1; k < len(ms); k++ {
		prev, cur := ms[k-1], ms[k]
		if prev.offset+prev.length > cur.offset {
			i, j := prev.idx, cur.idx
			if i > j {
				i, j = j, i
			}
			return "", 0, &EditError{Phase: "overlap", Index: i,
				Text: fmt.Sprintf("edits[%d] and edits[%d] overlap. "+
					"Merge them into one edit or target disjoint regions.", i, j)}
		}
	}

	// ---- Apply. Because matches are disjoint and sorted, splicing right to
	// left keeps every remaining offset valid without recomputing anything.
	out := content
	for k := len(ms) - 1; k >= 0; k-- {
		m := ms[k]
		out = out[:m.offset] + edits[m.idx].NewString + out[m.offset+m.length:]
	}

	// ---- Phase 5: no-op. A batch that changes nothing is a rejection, not a
	// silent success: it means the model believes it edited something it did
	// not, and reporting success would let it move on.
	if out == content {
		return "", 0, &EditError{Phase: "noop",
			Text: "The edits would leave the file unchanged."}
	}
	return out, len(edits), nil
}

// normalizeEdits mirrors NormalizeForEdit's CRLF rule onto the edits.
func normalizeEdits(edits []Edit) []Edit {
	out := make([]Edit, len(edits))
	for i, e := range edits {
		out[i] = Edit{
			OldString: strings.ReplaceAll(e.OldString, "\r\n", "\n"),
			NewString: strings.ReplaceAll(e.NewString, "\r\n", "\n"),
		}
	}
	return out
}

// notFoundHint is what makes a not_found rejection ACTIONABLE without a
// re-read. It reports whether the needle's first non-blank line occurs in the
// file and where, so the model can tell "I have the wrong file" from "the
// lines after my anchor have changed" from "my indentation is off" — the
// three failures that produce this rejection, each with a different fix.
func notFoundHint(content, old string) string {
	first := ""
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			first = l
			break
		}
	}
	if first == "" {
		return ""
	}
	key := FoldLine(first)
	loose := strings.TrimSpace(key)
	var exact, indented []int
	for n, l := range strings.Split(content, "\n") {
		switch fl := FoldLine(l); {
		case fl == key:
			exact = append(exact, n+1)
		case strings.TrimSpace(fl) == loose:
			indented = append(indented, n+1)
		}
	}
	const show = 3
	lines := func(ns []int) string {
		parts := make([]string, 0, show)
		for i, n := range ns {
			if i == show {
				parts = append(parts, "…")
				break
			}
			parts = append(parts, fmt.Sprint(n))
		}
		return strings.Join(parts, ", ")
	}
	multi := strings.Contains(strings.TrimSpace(old), "\n")
	switch {
	case len(exact) > 0 && multi:
		return fmt.Sprintf(" Its first line occurs at line %s; the lines after it differ from the file.", lines(exact))
	case len(exact) > 0:
		// A single-line needle whose folded form is present but whose exact
		// bytes are not: the fold failed for another edit in the batch, or the
		// difference is one FoldLine does not cover.
		return fmt.Sprintf(" A near match is at line %s; the difference is in whitespace or punctuation.", lines(exact))
	case len(indented) > 0:
		return fmt.Sprintf(" Its first line occurs at line %s with different indentation.", lines(indented))
	}
	return " Not even its first line occurs in the file; re-read the file before retrying."
}

// countOverlapping counts every occurrence of sub in s, INCLUDING overlapping
// ones. strings.Count counts non-overlapping occurrences, so it reports "aa"
// as unique in "aaa" — and there are two sites the model could have meant,
// which is exactly the ambiguity REQ-TOOL-04c's uniqueness rule exists to
// reject.
func countOverlapping(s, sub string) int {
	n := 0
	for i := 0; ; n++ {
		j := strings.Index(s[i:], sub)
		if j < 0 {
			return n
		}
		i += j + 1
	}
}

// LineEnding describes the convention a file uses.
type LineEnding string

const (
	LF   LineEnding = "lf"
	CRLF LineEnding = "crlf"
)

// NormalizeForEdit strips a leading BOM and converts CRLF to LF so matching
// works against what the model actually saw, and reports what it removed so
// Restore can put it back (REQ-TOOL-04d).
//
// The CRLF restoration is LOSSY for a mixed-ending file: if the original
// contains ANY CRLF the output is CRLF throughout (ruling P-24). That is a
// deliberate, reported choice — preserving per-line endings would require
// tracking them through every splice, and a file with mixed endings is
// already in a state no editor preserves faithfully.
func NormalizeForEdit(s string) (normalized string, bom bool, ending LineEnding) {
	ending = LF
	if strings.HasPrefix(s, "\ufeff") {
		bom = true
		s = strings.TrimPrefix(s, "\ufeff")
	}
	if strings.Contains(s, "\r\n") {
		ending = CRLF
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	return s, bom, ending
}

// Restore re-applies what NormalizeForEdit removed.
func Restore(s string, bom bool, ending LineEnding) string {
	if ending == CRLF {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	if bom {
		s = "\ufeff" + s
	}
	return s
}

// applyEditsFolded is REQ-TOOL-04d's whitespace-tolerant pass.
//
// It runs only after exact matching has failed for the batch, and it reports
// failure by returning ok=false rather than its own error: the caller then
// emits the ORIGINAL phase-ordered rejection. A second, differently-worded
// error from a fallback the model never asked for would tell it to fix the
// wrong thing.
//
// Only the edits exact matching could NOT find are folded. An edit that was
// found exactly keeps exact semantics here too — it must be unique and it
// replaces exactly its bytes — so batching it with an edit that needs the
// fold neither widens it to whole lines nor lets an ambiguous match through
// that would be rejected as not unique on its own.
//
// A folded edit matches per LINE BLOCK and the splice puts back whole
// ORIGINAL lines, so every line outside a matched block keeps its exact bytes
// — the curly quotes and trailing spaces that made the fold necessary in the
// first place survive untouched. The fold is only ever a key.
func applyEditsFolded(content string, edits []Edit) (string, int, bool) {
	lines := strings.Split(content, "\n")
	folded := foldLines(lines)
	// lineStart[i] is the byte offset of line i; lineStart[len(lines)] is
	// one past the end, as if the content ended in a newline.
	lineStart := make([]int, len(lines)+1)
	for i, l := range lines {
		lineStart[i+1] = lineStart[i] + len(l) + 1
	}

	type span struct {
		idx        int
		start, end int // byte range, half-open
	}
	spans := make([]span, 0, len(edits))

	for i, e := range edits {
		if strings.Contains(content, e.OldString) {
			if countOverlapping(content, e.OldString) != 1 {
				return "", 0, false // not unique: the exact path's rejection stands
			}
			off := strings.Index(content, e.OldString)
			spans = append(spans, span{idx: i, start: off, end: off + len(e.OldString)})
			continue
		}
		// A needle that begins or ends with a newline: the empty piece the
		// split leaves at that end is not a line. A trailing one says the
		// needle runs THROUGH the last line's newline, to the start of the
		// next line; a leading one says it starts at the END of the line
		// before. Matched as a line, it folded to "" and claimed a
		// whitespace-only neighbour, whose bytes the splice then dropped.
		parts := strings.Split(e.OldString, "\n")
		lead := len(parts) > 1 && parts[0] == ""
		if lead {
			parts = parts[1:]
		}
		trail := len(parts) > 1 && parts[len(parts)-1] == ""
		if trail {
			parts = parts[:len(parts)-1]
		}
		// A leading newline needs a line before the match, a trailing one a
		// newline after it: the last element of lines has none.
		lo, hi := 0, len(folded)
		if lead {
			lo = 1
		}
		if trail {
			hi = len(folded) - 1
		}
		start, end, count := 0, 0, 0
		if lo < hi {
			start, end, count = findFoldedBlock(folded[lo:hi], foldLines(parts))
			start, end = start+lo, end+lo
		}
		// Every edit must match, and match exactly once. A batch where the
		// fold rescues some edits and not others is not a batch the model
		// meant, and applying the subset would be the silent partial
		// application REQ-TOOL-04's phases exist to prevent.
		if count != 1 {
			return "", 0, false
		}
		// Lines [start, end): from the first line's start to the last line's
		// end, its newline excluded — widened by the newline before or after
		// when the needle carries one.
		sp := span{idx: i, start: lineStart[start], end: lineStart[end] - 1}
		if lead {
			sp.start--
		}
		if trail {
			sp.end++
		}
		spans = append(spans, sp)
	}

	sort.Slice(spans, func(a, b int) bool { return spans[a].start < spans[b].start })
	for k := 1; k < len(spans); k++ {
		if spans[k-1].end > spans[k].start {
			return "", 0, false // overlapping: same rejection as an exact overlap
		}
	}

	// Splice right to left so earlier ranges stay valid.
	out := content
	for k := len(spans) - 1; k >= 0; k-- {
		sp := spans[k]
		out = out[:sp.start] + edits[sp.idx].NewString + out[sp.end:]
	}
	if out == content {
		return "", 0, false // no-op, rejected exactly as the exact path rejects one
	}
	return out, len(edits), true
}
