package openrouter

import (
	"net/url"
	"slices"
	"strings"
)

// newRedactor returns what replaces every occurrence of the key with
// "[redacted]": the key itself, and the key as a URL escapes it, which is how a
// request error shows a key that is in the base URL.
func newRedactor(key string) *strings.Replacer {
	forms := []string{key}
	for _, f := range []string{url.PathEscape(key), url.QueryEscape(key)} {
		if !slices.Contains(forms, f) {
			forms = append(forms, f)
		}
	}
	slices.SortStableFunc(forms, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, "[redacted]")
	}
	return strings.NewReplacer(pairs...)
}

// redact returns s with the API key taken out.
func (c *client) redact(s string) string { return c.redactor.Replace(s) }

// redacted is an error whose text has had the API key replaced; it still wraps
// the error it stands for, so errors.Is and errors.As see through it.
type redacted struct {
	msg string
	err error
}

func (e *redacted) Error() string { return e.msg }
func (e *redacted) Unwrap() error { return e.err }
