package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// SearchMatch is one hit (REQ-TOOL-05).
type SearchMatch struct {
	File   string   `json:"file"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitzero"`
	After  []string `json:"after,omitzero"`
}

// SearchParams is the declared parameter set.
type SearchParams struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	ContextLines int    `json:"context_lines"`
	FileGlob     string `json:"file_glob"`
	// CaseSensitive is a POINTER because absent and false are different
	// answers. Absent means smart-case; false means explicitly insensitive.
	// A plain bool would make "the caller said nothing" indistinguishable
	// from "the caller asked for insensitive", and smart-case is the
	// behaviour a user typing a lowercase pattern expects.
	CaseSensitive *bool `json:"case_sensitive"`
	MaxMatches    int   `json:"max_matches"`
}

// SearchResult is the returned envelope.
type SearchResult struct {
	Matches       []SearchMatch `json:"matches"`
	Truncated     bool          `json:"truncated"`
	FilesSearched int           `json:"files_searched"`
	// TruncatedBy names which limit fired when Truncated is set: the match
	// cap ("lines") or the 50 KB byte cap ("bytes") (REQ-TOOL-09).
	TruncatedBy TruncatedBy `json:"truncated_by,omitzero"`
}

// binarySniffBytes is how much of a file is examined for a NUL byte before a
// line is searched. A NUL in the first 64 KiB makes the file binary; a NUL
// after that ends the search at the line holding it.
const binarySniffBytes = 64 << 10

// MaxSearchContextLines bounds context_lines.
//
// Context multiplies the result: 100 matches with 50 lines either side is
// 10,000 lines the model pays for and did not ask for.
const MaxSearchContextLines = 20

// searchFiles is REQ-TOOL-05.
//
// Declared semantics. The search is native Go (a ripgrep backend was removed
// by PRD 09 step 5; codesearch is the large-repository path):
//
//   - case_sensitive absent means SMART-CASE: an all-lowercase pattern matches
//     insensitively, anything with an uppercase rune matches sensitively.
//   - Binary files are SKIPPED, decided by a NUL byte in the first 64 KiB; a
//     later NUL ends the file's search (binarySniffBytes).
//   - A file that starts with a byte-order mark is read as the text it marks:
//     a UTF-8 BOM is not part of line 1, and UTF-16 is transcoded. A PowerShell script saved by
//     Windows PowerShell is UTF-16, and was otherwise skipped as binary.
//   - Lines end at "\n", and a "\r" before it is not part of the line, so
//     `$` matches at the end of a CRLF line.
//   - file_glob uses AgentKit's glob dialect (MatchGlob), applied to the path
//     RELATIVE to the search root.
//   - The pattern is Go's regexp (RE2), and that includes its ASCII \w, \d,
//     \s and \b: `\w+` does not match `café` whole.
//   - Smart-case looks at the pattern's LITERAL characters only: `error\S`
//     is lowercase, because \S is an escape and not an uppercase letter.
//     The decision is made once (caseSensitive).
//   - files_searched counts the files SELECTED for search — everything left
//     after the ignore rules, the hidden-entry rule and file_glob. A binary
//     file is counted as selected and then skipped, because whether a file
//     turns out to be binary is not something the caller can predict from the
//     query. The count does not
//     change when the result is truncated: a truncated native search stops
//     READING files but finishes the walk, because a number that meant
//     "selected" on one query and "examined before we gave up" on another
//     would be a number nobody can use. The walk is readdir plus pattern
//     matching; the file reads are what truncation exists to avoid.
//   - HIDDEN entries — any path component beginning with "." — are skipped,
//     which keeps `.git/` internals and
//     `.env` out of a result the model reads.
//   - A git repository is NOT required. Ignore rules are applied wherever they
//     are found; with no .gitignore anywhere, every non-hidden, non-binary
//     file is searched.
func (f *fileTools) searchFiles() core.Tool {
	return core.Tool{
		Name: "search_files",
		OutputSchema: schema.Object(
			schema.Prop("matches", schema.Array(schema.Object(
				schema.Prop("file", schema.String()),
				schema.Prop("line", schema.Int()),
				schema.Prop("text", schema.String()),
				schema.Opt("before", schema.Array(schema.String())),
				schema.Opt("after", schema.Array(schema.String())),
			))),
			schema.Prop("truncated", schema.Bool()),
			schema.Prop("files_searched", schema.Int()),
			schema.Opt("note", schema.String()),
		),
		Description: "Search file contents by regular expression, skipping .gitignored, " +
			"hidden (dot-prefixed) and binary files. Returns at most max_matches (<= 100) " +
			"matches with context_lines (<= 20) lines either side.",
		Builtin: true,
		// The "prefer search_files over execute+grep" guideline compares two
		// tools, so it is emitted by the prompt builder only when both are
		// present (tools.SearchOverExecuteGuideline).
		// max_matches above the cap is clamped to it, as it always was
		// (effectiveMax), rather than refused by the schema's maximum: asking
		// for more than the tool returns is not an error worth a turn.
		PrepareArguments: func(args map[string]any) map[string]any {
			var n float64
			switch v := args["max_matches"].(type) {
			case json.Number: // the argument map's number form
				f, err := v.Float64()
				if err != nil {
					return args
				}
				n = f
			case float64:
				n = v
			default:
				return args
			}
			if n <= SearchMatchCap {
				return args
			}
			out := make(map[string]any, len(args))
			for k, v := range args {
				out[k] = v
			}
			out["max_matches"] = SearchMatchCap
			return out
		},
		InputSchema: schema.Object(
			schema.Prop("pattern", schema.String("Regular expression (RE2 syntax)")),
			schema.Opt("path", schema.String("Directory to search from (default the workspace root)")),
			schema.Opt("context_lines", schema.Int(fmt.Sprintf(
				"Lines of context either side of a match (0 to %d)", MaxSearchContextLines)).
				Min(0).Max(MaxSearchContextLines)),
			schema.Opt("file_glob", schema.String("Only search files matching this glob, e.g. **/*.py or src/**/*.ts")),
			schema.Opt("case_sensitive", schema.Bool("Omit for smart-case: a lowercase pattern matches any case")),
			schema.Opt("max_matches", schema.Int(fmt.Sprintf(
				"Maximum matches to return (1 to %d; default %d)", SearchMatchCap, SearchMatchCap)).
				Min(1).Max(SearchMatchCap)),
		),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			var a SearchParams
			if err := json.Unmarshal(in, &a); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			if strings.TrimSpace(a.Pattern) == "" {
				return core.ErrResult("invalid_arguments", "pattern is required")
			}
			if a.Path == "" {
				a.Path = "."
			}
			root, err := f.ws.Resolve(a.Path)
			if err != nil {
				return core.ErrResult("path_not_allowed", err.Error())
			}
			if a.ContextLines < 0 || a.ContextLines > MaxSearchContextLines {
				return core.ErrResult("invalid_arguments", fmt.Sprintf(
					"context_lines must be between 0 and %d (got %d); retry with context_lines=%d or less, "+
						"or omit it for no context",
					MaxSearchContextLines, a.ContextLines, MaxSearchContextLines))
			}

			res, err := SearchIn(ctx, root, a, f.ig)
			if err != nil {
				if ctx.Err() != nil {
					return core.ErrResult("aborted", "Operation aborted")
				}
				var perr *SearchPatternError
				if errors.As(err, &perr) {
					return core.ErrResult("invalid_arguments", perr.Error())
				}
				return core.ErrResult("search_failed", err.Error())
			}

			out := core.OKResult(map[string]any{
				"matches":        res.Matches,
				"truncated":      res.Truncated,
				"files_searched": res.FilesSearched,
			})
			note := ""
			if res.Truncated {
				// The marker names THIS tool's parameter, max_matches, and its
				// cap of 100 — not find_files' `limit` (REQ-TOOL-09b).
				note = SearchMarker(effectiveMax(a.MaxMatches))
				if res.TruncatedBy == TruncatedByBytes {
					note = SearchBytesMarker(len(res.Matches), DefaultByteLimit)
				}
				out.Data["note"] = note
				out.Metadata = &core.ToolMetadata{
					Truncated: true, TruncatedBy: string(res.TruncatedBy),
				}
			}
			out.Text = RenderSearchText(res, note)
			return out
		},
	}
}

// RenderSearchText is the model-facing rendering of a search result: grep
// style, grouped by file.
//
//	path/to/file.go
//	  12- context before
//	  13: the matched line
//	  14- context after
//
//	other/file.go
//	  7: another match
//	[marker]
//
// It replaces the JSON envelope for the model (core.ToolResult.Text). Per
// match the envelope repeats the file name and the four keys, and every line
// of code inside it is JSON-escaped; grep's shape says the file once per
// group and the line once, unescaped, which for a typical result is a third
// fewer bytes and a better tokenization of the code itself. Data keeps the
// structured form for programmatic consumers.
//
// Within a file each line number is printed once: a line that is both a match
// and another match's context is shown as the match.
func RenderSearchText(res SearchResult, marker string) string {
	if len(res.Matches) == 0 {
		return fmt.Sprintf("No matches (%d files searched).", res.FilesSearched)
	}
	type line struct {
		text  string
		match bool
	}
	var (
		b       strings.Builder
		files   []string
		perFile = map[string]map[int]line{}
	)
	for _, m := range res.Matches {
		lines, ok := perFile[m.File]
		if !ok {
			lines = map[int]line{}
			perFile[m.File] = lines
			files = append(files, m.File)
		}
		put := func(n int, text string, match bool) {
			if prev, seen := lines[n]; seen && prev.match && !match {
				return
			}
			lines[n] = line{text, match}
		}
		for i, t := range m.Before {
			put(m.Line-len(m.Before)+i, t, false)
		}
		put(m.Line, m.Text, true)
		for i, t := range m.After {
			put(m.Line+1+i, t, false)
		}
	}
	for fi, f := range files {
		if fi > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(f)
		b.WriteByte('\n')
		lines := perFile[f]
		nums := make([]int, 0, len(lines))
		for n := range lines {
			nums = append(nums, n)
		}
		sort.Ints(nums)
		for _, n := range nums {
			l := lines[n]
			sep := '-'
			if l.match {
				sep = ':'
			}
			fmt.Fprintf(&b, "  %d%c %s\n", n, sep, l.text)
		}
	}
	if marker != "" {
		b.WriteString(marker)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// SearchPatternError is a bad regular expression, separated so the tool can
// report it as invalid arguments rather than as a failed search — the caller
// has to change the pattern, not retry.
type SearchPatternError struct{ Err error }

func (e *SearchPatternError) Error() string {
	return "invalid pattern: " + e.Err.Error() +
		`. The pattern is a regular expression (RE2): write a literal metacharacter with a ` +
		`backslash, for example \( for a literal ( or \. for a literal .`
}
func (e *SearchPatternError) Unwrap() error { return e.Err }

func effectiveMax(n int) int {
	if n <= 0 || n > SearchMatchCap {
		return SearchMatchCap
	}
	return n
}

// Search runs the search.
//
// It is exported so an embedder can search without going through the tool
// envelope. It reads
// the real ignore environment; SearchIn takes an explicit one.
func Search(ctx context.Context, root string, p SearchParams) (SearchResult, error) {
	return SearchIn(ctx, root, p, IgnoreOptions{})
}

// SearchIn is Search with an injected ignore environment (NFR-TEST-04), so a
// test can pin an empty global excludes layer instead of inheriting the
// developer's.
func SearchIn(ctx context.Context, root string, p SearchParams, ig IgnoreOptions) (SearchResult, error) {
	res, err := searchNative(ctx, root, p, ig)
	if err != nil {
		return res, err
	}
	// REQ-TOOL-09: the 50 KB byte limit composes with the match cap. 100 matches with 20 lines of context either side at 500
	// chars a line is two megabytes; without this the model paid for it.
	capSearchBytes(&res, DefaultByteLimit)
	return res, nil
}

// capSearchBytes applies the head-mode byte budget over the assembled matches,
// dropping whole matches from the end until the payload fits, and records
// which limit fired.
func capSearchBytes(res *SearchResult, budget int) {
	if res.Truncated {
		res.TruncatedBy = TruncatedByLines
	}
	used := 0
	for i, m := range res.Matches {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		// +1 for the separator; the envelope's own keys are a rounding error
		// next to the budget.
		used += len(b) + 1
		if used > budget && i > 0 {
			res.Matches = res.Matches[:i]
			res.Truncated, res.TruncatedBy = true, TruncatedByBytes
			return
		}
	}
}

// compilePattern applies the smart-case rule.
func compilePattern(p SearchParams) (*regexp.Regexp, error) {
	pat := p.Pattern
	if !caseSensitive(p) {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, &SearchPatternError{Err: err}
	}
	return re, nil
}

// caseSensitive resolves the tri-state.
func caseSensitive(p SearchParams) bool {
	if p.CaseSensitive != nil {
		return *p.CaseSensitive
	}
	// Smart-case: an uppercase LITERAL makes the pattern sensitive.
	return hasUppercaseLiteral(p.Pattern)
}

// hasUppercaseLiteral reports whether a pattern contains an uppercase letter
// that the pattern means literally. Escapes (\S, \W, \D, \B, \A, \p{Lu}),
// flag groups ((?U)) and group names ((?P<Name>...)) are syntax, not text:
// `error\S` asks for "error" in any case, as ripgrep's --smart-case reads it.
// Text between \Q and \E is literal.
func hasUppercaseLiteral(pat string) bool {
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; {
		case c == '\\' && i+1 < len(pat):
			i++
			switch pat[i] {
			case 'Q':
				end := strings.Index(pat[i+1:], `\E`)
				if end < 0 {
					end = len(pat) - i - 1
				}
				if strings.ToLower(pat[i+1:i+1+end]) != pat[i+1:i+1+end] {
					return true
				}
				i += end + 2
			case 'p', 'P', 'x':
				if i+1 < len(pat) && pat[i+1] == '{' {
					if end := strings.IndexByte(pat[i:], '}'); end > 0 {
						i += end
					}
				} else if pat[i] != 'x' {
					i++ // \pL: a one-letter class name
				}
			}
		case c == '(' && strings.HasPrefix(pat[i:], "(?"):
			j := i + 2
			if strings.HasPrefix(pat[j:], "P<") || (strings.HasPrefix(pat[j:], "<") && !strings.HasPrefix(pat[j:], "<=") && !strings.HasPrefix(pat[j:], "<!")) {
				if end := strings.IndexByte(pat[j:], '>'); end > 0 {
					i = j + end
				}
				continue
			}
			for j < len(pat) && (pat[j] == '-' || unicode.IsLetter(rune(pat[j]))) {
				j++
			}
			i = j - 1
		default:
			r, size := utf8.DecodeRuneInString(pat[i:])
			if unicode.IsUpper(r) {
				return true
			}
			i += size - 1
		}
	}
	return false
}

// ---------------------------------------------------------------- native

func searchNative(ctx context.Context, root string, p SearchParams, igOpts IgnoreOptions) (SearchResult, error) {
	re, err := compilePattern(p)
	if err != nil {
		return SearchResult{}, err
	}
	max := effectiveMax(p.MaxMatches)

	out := SearchResult{Matches: []SearchMatch{}}

	walkErr := walk(ctx, root, igOpts, false, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			// A symlink, socket or device is not a file to grep. Following a
			// symlink here is also how a search inside a workspace reads
			// outside one.
			return nil
		}
		if p.FileGlob != "" && !MatchGlob(p.FileGlob, rel) {
			return nil
		}

		abs := filepath.Join(root, filepath.FromSlash(rel))
		out.FilesSearched++
		if out.Truncated {
			return nil // counted, but the result is already full
		}
		found, ferr := searchFile(abs, rel, re, p.ContextLines, max-len(out.Matches))
		if ferr != nil {
			return nil // an unreadable file is skipped
		}
		if found.skipped {
			return nil // binary: selected and counted, but not scanned
		}
		out.Matches = append(out.Matches, found.matches...)
		if len(out.Matches) >= max {
			out.Truncated = true
		}
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return SearchResult{}, ctx.Err()
	}
	if len(out.Matches) > max {
		out.Matches, out.Truncated = out.Matches[:max], true
	}
	return out, nil
}

// isHidden reports a dotfile or dot-directory.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

type fileMatches struct {
	matches []SearchMatch
	skipped bool // binary
}

func searchFile(abs, rel string, re *regexp.Regexp, contextLines, budget int) (fileMatches, error) {
	if budget <= 0 {
		return fileMatches{}, nil
	}
	fh, err := os.Open(abs)
	if err != nil {
		return fileMatches{}, err
	}
	defer fh.Close()

	br := textReader(fh)
	head, err := br.Peek(binarySniffBytes)
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && len(head) == 0 && err.Error() != "EOF" {
		return fileMatches{}, err
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return fileMatches{skipped: true}, nil
	}

	// The whole file is read line by line, keeping only a ring of `before`
	// lines. Reading it into memory would be simpler and would also mean a
	// 2 GB log file is a 2 GB allocation.
	var (
		out    fileMatches
		before = make([]string, 0, contextLines)
		// pending holds matches still collecting their `after` lines.
		pending []*SearchMatch
		lineNo  int
	)
	// Lines are read whole, however long. A bounded scanner stopped at the
	// first line past its limit — a minified bundle's single line — and the
	// file's remaining lines went unsearched, where ripgrep searches them.
	var buf []byte
	for {
		raw, err := readWholeLine(br, buf[:0])
		if err != nil {
			break // io.EOF, or a read error: report what was found
		}
		buf = raw
		if bytes.IndexByte(raw, 0) >= 0 {
			break // a NUL past the sniff window: the rest is binary (binarySniffBytes)
		}
		lineNo++
		line := string(raw)

		for i := 0; i < len(pending); {
			m := pending[i]
			if len(m.After) < contextLines {
				m.After = append(m.After, capLine(line))
				i++
				continue
			}
			pending = append(pending[:i], pending[i+1:]...)
		}

		if re.MatchString(line) && len(out.matches) < budget {
			out.matches = append(out.matches, SearchMatch{
				File: rel, Line: lineNo, Text: capLine(line),
				Before: append([]string(nil), before...),
			})
			if contextLines > 0 {
				pending = append(pending, &out.matches[len(out.matches)-1])
			}
		}

		if contextLines > 0 {
			before = append(before, capLine(line))
			if len(before) > contextLines {
				before = before[1:]
			}
		}
		if len(out.matches) >= budget && len(pending) == 0 {
			break
		}
	}
	return out, nil
}

// textReader is the reader a file is searched through: the text its
// byte-order mark declares, without the mark. No mark means the bytes as
// they are.
func textReader(r io.Reader) *bufio.Reader {
	raw := bufio.NewReaderSize(r, binarySniffBytes)
	bom, _ := raw.Peek(3)
	switch {
	case bytes.HasPrefix(bom, []byte{0xEF, 0xBB, 0xBF}):
		_, _ = raw.Discard(3)
	case bytes.HasPrefix(bom, []byte{0xFF, 0xFE}):
		_, _ = raw.Discard(2)
		return bufio.NewReaderSize(&utf16Reader{r: raw, little: true}, binarySniffBytes)
	case bytes.HasPrefix(bom, []byte{0xFE, 0xFF}):
		_, _ = raw.Discard(2)
		return bufio.NewReaderSize(&utf16Reader{r: raw}, binarySniffBytes)
	}
	return raw
}

// utf16Reader transcodes UTF-16 to UTF-8. An unpaired surrogate, or a
// trailing odd byte, becomes U+FFFD.
type utf16Reader struct {
	r       *bufio.Reader
	little  bool
	out     []byte // encoded, not yet returned
	pending rune   // a unit read past an unpaired high surrogate, when held
	held    bool
}

func (u *utf16Reader) Read(p []byte) (int, error) {
	for len(u.out) == 0 {
		r, err := u.next()
		if err != nil {
			return 0, err
		}
		if utf16.IsSurrogate(r) {
			hi := r
			r = utf8.RuneError
			if hi < 0xDC00 { // a high surrogate wants a low one next
				lo, err := u.next()
				switch {
				case err != nil:
				case lo >= 0xDC00 && lo <= 0xDFFF:
					r = utf16.DecodeRune(hi, lo)
				default:
					u.pending, u.held = lo, true // not ours: decode it next
				}
			}
		}
		u.out = utf8.AppendRune(u.out, r)
	}
	n := copy(p, u.out)
	u.out = u.out[n:]
	return n, nil
}

// next returns a held unit, or reads one.
func (u *utf16Reader) next() (rune, error) {
	if u.held {
		u.held = false
		return u.pending, nil
	}
	return u.unit()
}

// unit reads one 16-bit code unit.
func (u *utf16Reader) unit() (rune, error) {
	var b [2]byte
	n, err := io.ReadFull(u.r, b[:])
	switch {
	case n == 1:
		return utf8.RuneError, nil
	case err != nil:
		return 0, err
	case u.little:
		return rune(b[0]) | rune(b[1])<<8, nil
	}
	return rune(b[0])<<8 | rune(b[1]), nil
}

// readWholeLine reads the next line from br into buf, without its "\n" or a
// trailing "\r", whatever its length. It returns io.EOF only when there is
// no further data; a last line with no newline is returned with a nil error.
func readWholeLine(br *bufio.Reader, buf []byte) ([]byte, error) {
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			buf = buf[:len(buf)-1]
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
		default:
			return nil, err
		}
		if n := len(buf); n > 0 && buf[n-1] == '\r' {
			buf = buf[:n-1]
		}
		return buf, nil
	}
}

// capLine bounds one returned line (REQ-TOOL-09's per-line cap).
//
// The cap is 500 CHARACTERS, cut on a rune boundary. A byte slice at 500
// lands inside a multi-byte rune one time in a few, and the result is
// invalid UTF-8 that a JSON encoder replaces with U+FFFD — a line the model
// then cannot match back against the file.
func capLine(s string) string {
	if len(s) <= SearchLineChars {
		return s // at most 500 bytes is at most 500 runes
	}
	n := 0
	for i := range s {
		if n == SearchLineChars {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// CountCandidates counts the files a search would select: everything left
// after the ignore rules, the hidden-entry rule and file_glob.
func CountCandidates(ctx context.Context, root string, p SearchParams) (int, error) {
	return countCandidates(ctx, root, p, IgnoreOptions{})
}

func countCandidates(ctx context.Context, root string, p SearchParams, igOpts IgnoreOptions) (int, error) {
	n := 0
	err := walk(ctx, root, igOpts, false, func(rel string, d fs.DirEntry) error {
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if p.FileGlob != "" && !MatchGlob(p.FileGlob, rel) {
			return nil
		}
		n++
		return nil
	})
	return n, err
}
