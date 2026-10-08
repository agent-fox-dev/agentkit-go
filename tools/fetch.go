package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
)

// FetchResponseCap is REQ-TOOL-07's 512 KB response cap.
//
// It is enforced with a LimitReader plus a one-byte probe rather than by
// trusting Content-Length: a server that lies about the length, or sends none
// at all under chunked encoding, would otherwise stream unbounded bytes into
// the model's context and the process's memory.
const FetchResponseCap = 512 << 10

// FetchMaxRedirects is REQ-TOOL-07's 5-hop limit.
const FetchMaxRedirects = 5

// fetchHeaderValueCap bounds each returned header value. The headers that
// reach the model are a curated set (see returnedHeaders), and even those are
// capped: a Location or Cache-Control of several kilobytes is a payload, not
// metadata.
const fetchHeaderValueCap = 256

// returnedHeaders is the set of response headers the tool hands to the model.
// Everything else — cookies, CSP policies, server fingerprints, the several
// kilobytes of tracking headers a CDN adds — is dropped: the model cannot act
// on them, they cost context on every fetch, and a server that wants to feed
// the model text has the body for that.
var returnedHeaders = []string{
	"content-type", "content-length", "location", "last-modified", "etag", "cache-control",
}

// FetchOptions configures the fetch_url tool.
type FetchOptions struct {
	// AllowHTTP is REQ-SEC-09's opt-in (`tools.allow_http`). Off by default.
	AllowHTTP bool
	// Guard is the SSRF guard. Nil builds one from AllowHTTP. http:// is
	// permitted when either this AllowHTTP or the Guard's is set.
	Guard *SSRFGuard
	// DefaultTimeout applies when the call supplies no timeout_s.
	DefaultTimeout time.Duration
}

// FetchTool is REQ-TOOL-07.
//
// It is NOT in the default set (see All): an embedder registers it through
// ToolPolicy.CustomTools. That placement is the requirement, and the reason is
// that a tool which makes outbound requests on the model's behalf is a
// different risk class from one that reads a file inside a workspace root.
//
// REQ-SEC-01's path containment does not apply here and neither does the
// workspace root; the boundary for this tool is the SSRF guard and the scheme
// check, and the interceptor of REQ-SEC-03 above them.
func FetchTool(opts FetchOptions) core.Tool {
	guard := opts.Guard
	if guard == nil {
		guard = &SSRFGuard{AllowHTTP: opts.AllowHTTP}
	}
	// The opt-in holds wherever it was stated. A Guard supplied for its
	// resolver or dialer used to replace FetchOptions.AllowHTTP with its own
	// (usually false), silently.
	allowHTTP := opts.AllowHTTP || guard.AllowHTTP
	timeout := opts.DefaultTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{
		Transport: guard.Transport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= FetchMaxRedirects {
				return fmt.Errorf("tools: stopped after %d redirects", FetchMaxRedirects)
			}
			if err := checkScheme(req.URL, allowHTTP); err != nil {
				// Per-hop scheme re-validation. Without it an https URL
				// redirects to http and the guard's HTTPS-only promise holds
				// for exactly one hop.
				return err
			}
			// Caller-supplied headers are DROPPED on a cross-host redirect.
			// net/http already strips Authorization and Cookie, but a caller's
			// own X-Api-Key is not sensitive to it — and an open redirect is
			// how that key reaches somebody else's server.
			if via[0].URL.Host != req.URL.Host {
				for name := range req.Header {
					if !hopSafeHeader(name) {
						req.Header.Del(name)
					}
				}
			}
			return nil
		},
	}

	return core.Tool{
		Name: "fetch_url",
		Description: "Fetch a URL over HTTPS and return its body. Private, loopback, " +
			"link-local and reserved addresses are refused.",
		Builtin: true,
		// The method is case-insensitive here, as it always was: the enum is
		// upper-case, and a model writing "get" is not wrong about anything
		// but case (REQ-TOOL-11.1's repair, ahead of the enum check).
		PrepareArguments: func(args map[string]any) map[string]any {
			m, ok := args["method"].(string)
			if !ok || m == strings.ToUpper(m) {
				return args
			}
			out := make(map[string]any, len(args))
			for k, v := range args {
				out[k] = v
			}
			out["method"] = strings.ToUpper(m)
			return out
		},
		InputSchema: schema.Object(
			schema.Prop("url", schema.String("Absolute https:// URL")),
			schema.Opt("method", schema.Enum("HTTP method", "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE")),
			schema.Opt("headers", schema.Object().Describe("Request headers")),
			schema.Opt("body", schema.String("Request body")),
			schema.Opt("timeout_s", schema.Int("Request timeout in seconds")),
			schema.Opt("as_text", schema.Bool("Extract readable text from an HTML response")),
		),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			var a struct {
				URL      string            `json:"url"`
				Method   string            `json:"method"`
				Headers  map[string]string `json:"headers"`
				Body     string            `json:"body"`
				TimeoutS int               `json:"timeout_s"`
				AsText   bool              `json:"as_text"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}

			u, err := url.Parse(a.URL)
			if err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			if err := checkScheme(u, allowHTTP); err != nil {
				return core.ErrResult("scheme_not_allowed", err.Error())
			}

			method := strings.ToUpper(a.Method)
			if method == "" {
				method = http.MethodGet
			}

			d := timeout
			if a.TimeoutS > 0 {
				d = time.Duration(a.TimeoutS) * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()

			var body io.Reader
			if a.Body != "" {
				body = strings.NewReader(a.Body)
			}
			req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
			if err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}
			for k, v := range a.Headers {
				req.Header.Set(k, v)
			}

			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				if errors.Is(err, ErrBlockedAddress) {
					return core.ErrResult("address_not_allowed", err.Error())
				}
				if errors.Is(err, ErrSchemeNotAllowed) {
					return core.ErrResult("scheme_not_allowed", err.Error())
				}
				if ctx.Err() != nil {
					return core.ErrResult("timeout", err.Error())
				}
				return core.ErrResult("request_failed", err.Error())
			}
			defer resp.Body.Close()

			// Read one byte past the cap so truncation is DETECTED rather than
			// inferred from a body that happens to be exactly 512 KB.
			raw, rerr := io.ReadAll(io.LimitReader(resp.Body, FetchResponseCap+1))
			if rerr != nil {
				return core.ErrResult("request_failed", rerr.Error())
			}
			truncated := len(raw) > FetchResponseCap
			if truncated {
				raw = raw[:FetchResponseCap]
			}

			contentType := resp.Header.Get("Content-Type")
			headers := map[string]any{}
			for _, k := range returnedHeaders {
				if v := resp.Header.Get(k); v != "" {
					if len(v) > fetchHeaderValueCap {
						v = v[:fetchHeaderValueCap]
					}
					headers[k] = v
				}
			}
			data := map[string]any{
				"status":       resp.StatusCode,
				"url":          resp.Request.URL.String(),
				"content_type": contentType,
				"headers":      headers,
				"truncated":    truncated,
			}
			r := core.OKResult(data)
			r.Metadata = &core.ToolMetadata{
				Truncated: truncated, TotalBytes: int64(len(raw)),
				DurationMS: time.Since(start).Milliseconds(),
			}
			if truncated {
				r.Metadata.TruncatedBy = string(TruncatedByBytes)
				// The cut can land inside a multi-byte character; the partial
				// rune is dropped so a truncated text body is still text.
				raw = trimIncompleteRune(raw)
			}

			// A body that is not text is not returned as text. An image, a
			// zip or a PDF handed to the model as a string is several hundred
			// KB of mojibake it cannot read; it is told what arrived and how
			// big it was instead. The decision is made on the content type
			// where the server states one, and on the bytes where it does not
			// — a text/plain that is not valid UTF-8 is not text either.
			if !textualContentType(contentType) || !utf8.Valid(raw) {
				data["binary"] = true
				data["bytes"] = len(raw)
				return r
			}
			text := string(raw)
			if a.AsText && strings.Contains(strings.ToLower(contentType), "html") {
				text = HTMLToText(text)
			}
			data["body"] = text
			return r
		},
	}
}

// textualContentType reports whether a Content-Type names something the model
// can read as text. An absent or unparseable type is treated as textual and
// left to the UTF-8 check.
func textualContentType(ct string) bool {
	if strings.TrimSpace(ct) == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return true
	}
	typ, sub, _ := strings.Cut(strings.ToLower(mt), "/")
	switch {
	case typ == "text":
		return true
	case strings.HasSuffix(sub, "+json"), strings.HasSuffix(sub, "+xml"):
		return true
	}
	switch sub {
	case "json", "xml", "javascript", "ecmascript", "x-www-form-urlencoded",
		"x-ndjson", "ld+json", "graphql", "yaml", "x-yaml", "toml", "sql":
		return typ == "application"
	}
	return false
}

// trimIncompleteRune drops a trailing partial UTF-8 sequence left by a byte
// cut, so a body truncated at exactly the cap is still valid text.
func trimIncompleteRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if !utf8.FullRune(b[i:]) {
			return b[:i]
		}
		break
	}
	return b
}

func checkScheme(u *url.URL, allowHTTP bool) error {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if allowHTTP {
			return nil
		}
		return fmt.Errorf("%w (got %s)", ErrSchemeNotAllowed, u)
	}
	return fmt.Errorf("%w (got scheme %q)", ErrSchemeNotAllowed, u.Scheme)
}

// hopSafeHeader names the headers that may survive a cross-host redirect.
func hopSafeHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Accept", "Accept-Encoding", "Accept-Language", "User-Agent", "Content-Type":
		return true
	}
	return false
}

// HTMLToText is REQ-TOOL-07's optional extraction: the text of an HTML page,
// whitespace-collapsed, with script and style content dropped.
//
// It runs golang.org/x/net/html's tokenizer — the HTML5 tokenization rules —
// so entities are decoded exactly once (numeric ones included), a `>` inside
// an attribute value does not end the tag, comments are dropped, and an
// unclosed <script> runs to the end of the input. It is text extraction, not
// readability extraction.
func HTMLToText(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var b strings.Builder
	raw := false // inside <script> or <style>
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.TextToken:
			if !raw {
				b.Write(z.Text())
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			raw = string(name) == "script" || string(name) == "style"
			b.WriteByte(' ')
		case html.EndTagToken, html.SelfClosingTagToken:
			raw = false
			b.WriteByte(' ')
		}
	}
}
