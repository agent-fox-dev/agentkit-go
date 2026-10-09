package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/wire"
	"github.com/anthropics/anthropic-sdk-go/option"
	"golang.org/x/oauth2"
)

// APIVersion is the anthropic-version the SDK sends.
const APIVersion = "2023-06-01"

// VertexAPIVersion is the anthropic_version a Vertex request body carries.
const VertexAPIVersion = "vertex-2023-10-16"

// BetaCompaction opts into REQ-PROV-07's server-side compaction. core has no
// block to carry a compaction block, so the decoder drops it and later turns
// do not replay it.
const BetaCompaction = "compact-2026-01-12"

// Options configures the provider. The zero value is usable.
type Options struct {
	// Client is the official SDK client requests go through. Nil resolves one
	// from the environment.
	Client *sdk.Client

	BaseURL    string
	HTTPClient *http.Client
	// Getenv is injectable so a test never mutates process environment
	// (NFR-TEST-04). Nil means os.Getenv.
	Getenv func(string) string
	// MaxRetries overrides the SDK's retry count; nil keeps its default.
	MaxRetries *int
	// Betas are sent as anthropic-beta. BetaCompaction is REQ-PROV-07.
	Betas []string
	// VertexProject selects the Vertex AI deployment whatever the
	// environment says, and VertexLocation sets its region; both otherwise
	// come from the environment (see Resolve).
	VertexProject  string
	VertexLocation string
	// VertexTokenSource supplies the Google OAuth token a Vertex request is
	// authorized with. Nil uses a Google access token in ANTHROPIC_AUTH_TOKEN
	// when there is one, and Application Default Credentials otherwise.
	VertexTokenSource oauth2.TokenSource
	// BillingLookup resolves a SERVED model id to its catalog row
	// (REQ-PROV-05.5). Nil bills a fallback-served response at the requested
	// model's rates and still records the served name.
	BillingLookup func(string) *core.Model
	// ToolPrefix is REQ-CACHE-06's per-session schema cache. Nil means this
	// provider value owns one, which is the right scope in practice: a
	// registry is built per agent config. Pass one explicitly to share it, or
	// to read its reconciliation reports.
	ToolPrefix *provider.ToolPrefix
	// OnToolPrefixSync reports each reconciliation, so an embedder can feed
	// REQ-CACHE-11's prefix-invalidation counter.
	OnToolPrefixSync func(provider.SyncReport)
	// Now is injectable for deterministic timestamps in tests.
	Now func() time.Time

	// CacheRetention places the prompt-cache breakpoints; empty is short.
	CacheRetention core.CacheRetention
	// Headers merge into every request. A present-nil value is a DELETION
	// MARKER suppressing a default of that name (REQ-AUTH-02); no string
	// value can express that.
	Headers map[string]*string
	// Timeout bounds each request, independently of the caller's context
	// (REQ-PROV-18); zero is none.
	Timeout time.Duration
	// Env is consulted before Getenv when resolving the deployment
	// (REQ-AUTH-03). An empty override falls through rather than masking.
	Env map[string]string
	// Transport replaces the HTTP transport.
	Transport http.RoundTripper
	// OnPayload runs after canonical->wire translation and before the first
	// byte. Returning (nil, nil) leaves the payload unchanged; its error ends
	// the turn unmodified.
	OnPayload func(payload any, model *core.Model) (any, error)
	// OnResponse sees each HTTP response before it is read; its error ends
	// the turn.
	OnResponse func(resp *http.Response, model *core.Model) error
	// Warnf receives the transcript repair report; nil discards it.
	Warnf func(format string, args ...any)
}

// Provider returns a core.ProviderClient for the model m.
func Provider(m core.Model, opts Options) core.ProviderClient {
	c := &client{model: m, opts: opts, prefix: opts.ToolPrefix}
	if c.prefix == nil {
		c.prefix = &provider.ToolPrefix{}
	}
	return c
}

type client struct {
	model  core.Model
	opts   Options
	prefix *provider.ToolPrefix
}

// wantsCompaction reports whether Options.Betas opted into REQ-PROV-07.
func (c *client) wantsCompaction() bool {
	for _, b := range c.opts.Betas {
		if strings.TrimSpace(b) == BetaCompaction {
			return true
		}
	}
	return false
}

func (c *client) now() time.Time {
	if c.opts.Now != nil {
		return c.opts.Now()
	}
	return time.Now()
}

// Stream implements core.ProviderClient. A context already done is refused
// with its error; every failure after that is encoded in the stream — the
// failed turn's message and, on the last item, the error (REQ-PROV-04).
func (c *client) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := c.model
	return core.StreamChannel(c.stream(ctx, &m, req)), nil
}

func (c *client) stream(ctx context.Context, m *core.Model, req core.Request) *core.EventStream {
	retention := core.CacheRetentionShort
	if c.opts.CacheRetention != "" {
		retention = c.opts.CacheRetention
	}

	body, rep, sync, err := BuildRequestCached(m, req, retention, c.prefix)
	if err != nil {
		return core.ErrorStream(nil, fmt.Errorf("anthropic: building request: %w", err))
	}
	if fn := c.opts.OnToolPrefixSync; fn != nil {
		fn(sync)
	}
	if rep.Changed() && c.opts.Warnf != nil {
		c.opts.Warnf("anthropic: %s", rep.String())
	}
	if c.wantsCompaction() {
		// REQ-PROV-07: the beta header opts the REQUEST into the feature and
		// the body names the edit; the server compacts only when both are
		// present. A header alone was silently a no-op.
		body.ContextManagement = &contextManagement{Edits: []contextEdit{{Type: "compact_20260112"}}}
	}

	// REQ-PROV-18: OnPayload runs after canonical->wire translation and before
	// the first byte. Its error propagates to the caller UNMODIFIED, which is
	// why it is wrapped by ErrorStream rather than by fmt.Errorf.
	var payload any = body
	if fn := c.opts.OnPayload; fn != nil {
		out, perr := fn(body, m)
		if perr != nil {
			return core.ErrorStream(nil, perr)
		}
		if out != nil {
			payload = out
		}
	}

	var raw []byte
	if payload == any(body) {
		raw, err = encodeExact(body)
	} else {
		raw, err = json.Marshal(payload)
	}
	if err != nil {
		return core.ErrorStream(nil, fmt.Errorf("anthropic: encoding request: %w", err))
	}

	s := core.NewEventStream(core.StreamOptions{})
	go c.run(ctx, s, m, req, raw)
	return s
}

func (c *client) run(ctx context.Context, s *core.EventStream, m *core.Model, req core.Request,
	raw []byte) {
	d := &decodeState{
		s: s, model: m, lookup: c.opts.BillingLookup,
		partial: core.AssistantMessage{
			Model:     m.ID,
			Effort:    req.Effort,
			Timestamp: c.now(),
		},
		accs: map[int]*blockAcc{},
	}

	// caller is the ctx the caller handed to Stream; ctx below may be a
	// Timeout-derived child of it. The two are kept apart because their
	// expiries mean different things: the caller's is an abort (REQ-LOOP-09),
	// the derived one a retryable timeout (REQ-PROV-18).
	caller := ctx
	if to := c.opts.Timeout; to > 0 {
		// A per-request timeout INDEPENDENT of the caller's context deadline
		// (REQ-PROV-18). It must not outlive this function, hence the defer.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	sc, dep, env, err := c.sdkClient(c.opts.Env, c.opts.Transport)
	if err != nil {
		// A deployment that cannot be resolved — no credential, a Vertex
		// selection with no project — fails the turn with the reason.
		d.fail(err.Error(), err)
		return
	}

	// The body is ours: SDK params would re-encode replayed tool_use input,
	// and its bytes must reach the wire as the model wrote them.
	opts := []option.RequestOption{option.WithRequestBody("application/json", raw)}
	if rt := c.opts.Transport; rt != nil && c.opts.Client != nil {
		opts = append(opts, option.WithHTTPClient(&http.Client{Transport: rt}))
	}
	// A resolved client already carries MaxRetries; a caller's own does not.
	if n := c.opts.MaxRetries; n != nil && c.opts.Client != nil {
		opts = append(opts, option.WithMaxRetries(*n))
	}
	// REQ-AUTH-02: a request's own headers win, and a present-nil value
	// removes the header the provider would otherwise send — how a gateway
	// turns the upstream credential off.
	for k, v := range c.opts.Headers {
		if v == nil {
			opts = append(opts, option.WithHeaderDel(k))
		} else {
			opts = append(opts, option.WithHeader(k, *v))
		}
	}
	if fn := c.opts.OnResponse; fn != nil {
		opts = append(opts, option.WithMiddleware(func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
			resp, err := next(r)
			if err != nil {
				return resp, err
			}
			if herr := fn(resp, m); herr != nil {
				resp.Body.Close()
				return nil, herr
			}
			return resp, nil
		}))
	}

	stream := sc.Messages.NewStreaming(ctx, sdk.MessageNewParams{}, opts...)
	defer stream.Close()
	started := false
	for stream.Next() {
		ev := stream.Current()
		if !started {
			d.emitStart()
			started = true
		}
		if err := d.event(ev.Type, []byte(ev.RawJSON())); err != nil {
			d.fail(streamErrorText(caller, ctx, err), err)
			return
		}
		if d.sawStop {
			break
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) && caller.Err() == nil {
			text := statusText(apiErr) + vertexAuthNote(apiErr.StatusCode, dep, env)
			d.fail(text, &StatusError{Code: apiErr.StatusCode, Text: text})
			return
		}
		// A cancellation lands here as whatever the transport or the body
		// reader reported, and only the contexts say which of REQ-LOOP-09's
		// abort or REQ-PROV-18's timeout it was.
		d.fail(transportErrorText(caller, ctx, err), err)
		return
	}
	if !d.sawStop {
		// A 200 whose body simply stops is the single commonest streaming
		// failure, and only this check turns it into an error.
		d.fail(streamErrorText(caller, ctx, ErrStreamTruncated), ErrStreamTruncated)
		return
	}
	d.finish(m, c.opts.BillingLookup)
}

// vertexAuthNote explains an authentication failure from the Vertex
// deployment.
//
// Vertex answers a missing credential with a Google JSON blob — "Request is
// missing required authentication credential", CREDENTIALS_MISSING, a link to
// the Google sign-in console — that names neither Claude, nor the deployment,
// nor the setting that routed the request to Google. Rendered under this
// package's "anthropic:" prefix it reads as an Anthropic outage on a machine
// whose ANTHROPIC_API_KEY is perfectly good, and nothing in it suggests
// looking at CLAUDE_CODE_USE_VERTEX.
func vertexAuthNote(status int, dep deployment, env Env) string {
	if dep.source != SourceVertex || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return ""
	}
	project := dep.project
	if dep.projectVar != "" {
		project += " (from " + dep.projectVar + ")"
	}
	note := " [Claude on Vertex AI: project " + project + ", location " + dep.region +
		", selected by " + VertexEnableVar + " or Options.VertexProject. This deployment authenticates with " +
		"Google Application Default Credentials, not " + APIKeyVar + " or " + OAuthTokenVar
	var withheld []string
	for _, v := range []string{APIKeyVar, OAuthTokenVar} {
		if get(env, v) != "" {
			withheld = append(withheld, v)
		}
	}
	if len(withheld) > 0 {
		// Saying so is the whole point: the credential IS set, it was
		// deliberately withheld from a Google endpoint, and without this line
		// the operator reads the 401 as that credential being rejected.
		verb := " is set, and is"
		if len(withheld) > 1 {
			verb = " are set, and are"
		}
		note += " — " + strings.Join(withheld, " and ") + verb + " never sent to a Google endpoint"
	}
	return note + ". To use the Anthropic API directly instead, unset " +
		VertexEnableVar + " or set " + VertexEnableVar + "=0.]"
}

// AbortText is the one error string that means "the caller stopped this". It
// is matched by text because it has to survive a round trip through an
// AssistantMessage's ErrorMessage.
const AbortText = "Request was aborted"

// ErrStreamTruncated is a stream that ended before message_stop.
var ErrStreamTruncated = errors.New("agentkit: stream ended before message_stop")

// StatusError is a non-2xx response: the status code and the error text.
type StatusError struct {
	Code int
	Text string
}

func (e *StatusError) Error() string { return e.Text }

// statusText renders a non-2xx response with its status code, so a caller's
// retry policy that matches "429" or "503" sees them even when the body says
// nothing.
func statusText(e *sdk.Error) string {
	text := fmt.Sprintf("anthropic: HTTP %d", e.StatusCode)
	var we wireError
	if raw := e.RawJSON(); raw != "" && json.Unmarshal([]byte(raw), &we) == nil {
		if s := we.String(); s != "" {
			text += ": " + s
		}
	}
	return text
}

// transportErrorText renders a transport failure, classifying the two
// cancellation channels: the caller's (an abort) and the per-request timeout.
func transportErrorText(caller, req context.Context, err error) string {
	if text, ok := cancellationText(caller, req, err); ok {
		return text
	}
	return "anthropic: " + err.Error()
}

// streamErrorText is transportErrorText for a failure while the stream was
// being decoded, whose text already carries its prefix.
func streamErrorText(caller, req context.Context, err error) string {
	if text, ok := cancellationText(caller, req, err); ok {
		return text
	}
	return err.Error()
}

func cancellationText(caller, req context.Context, err error) (string, bool) {
	if caller.Err() != nil {
		return AbortText, true
	}
	if req.Err() != nil && errors.Is(req.Err(), context.DeadlineExceeded) {
		return "anthropic: request timeout (RequestOptions.TimeoutMs elapsed): " + err.Error(), true
	}
	return "", false
}

// ---------------------------------------------------------------- decode state

type decodeState struct {
	s       *core.EventStream
	partial core.AssistantMessage
	// model and lookup are what finish and fail price usage against
	// (REQ-PROV-05.5); fail needs them too, because a truncated stream that
	// reported usage in message_start still cost money.
	model  *core.Model
	lookup func(string) *core.Model

	accs       map[int]*blockAcc
	order      []int
	final      map[int]core.ContentBlock
	stopRaw    string
	stopDetail string
	stopSeq    string
	usage      core.Usage

	sawStop  bool
	salvaged int
}

func (d *decodeState) emitStart() { d.s.Push(core.MessageStartEvent{Message: d.partial}) }

func (d *decodeState) event(typ string, data []byte) error {
	ev := struct {
		Type string
		Data []byte
	}{typ, data}
	if ev.Type == "ping" || (ev.Type == "" && len(ev.Data) == 0) {
		return nil
	}
	// REQ-SEC-11: bytes a provider sent are bytes we did not produce. One
	// linear scan enforces the size, depth and container bounds and rejects
	// duplicate keys, which is what stops a gateway sending two stop_reasons
	// and letting last-wins choose which one we act on.
	if err := wire.Guard(ev.Data, wire.Limits{}); err != nil {
		return fmt.Errorf("anthropic: %s event: %w", ev.Type, err)
	}
	if ev.Type == "" {
		// No `event:` line. Every Messages payload also names its type in
		// the JSON, and a gateway that relays data lines without the event
		// line is a real thing; treating such a stream as a run of pings
		// ended every turn with ErrSSETruncated and no content.
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(ev.Data, &probe) != nil || probe.Type == "" || probe.Type == "ping" {
			return nil
		}
		ev.Type = probe.Type
	}
	switch ev.Type {

	case "error":
		var we wireError
		_ = json.Unmarshal(ev.Data, &we)
		msg := we.String()
		if msg == "" {
			msg = "anthropic: stream error"
		}
		return errors.New(msg)

	case "message_start":
		var p struct {
			Message wireResponse `json:"message"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return fmt.Errorf("anthropic: message_start: %w", err)
		}
		d.partial.ResponseID = p.Message.ID
		d.partial.ResponseModel = p.Message.Model
		p.Message.Usage.Into(&d.usage)

	case "content_block_start":
		var p struct {
			Index int             `json:"index"`
			Block json.RawMessage `json:"content_block"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return fmt.Errorf("anthropic: content_block_start: %w", err)
		}
		acc, err := startFrom(p.Block, false)
		if err != nil {
			return fmt.Errorf("anthropic: content_block_start: %w", err)
		}
		d.accs[p.Index] = acc
		d.order = append(d.order, p.Index)
		if e := acc.startEvent(p.Index); e != nil {
			d.s.Push(e)
		}

	case "content_block_delta":
		var p struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return fmt.Errorf("anthropic: content_block_delta: %w", err)
		}
		acc := d.accs[p.Index]
		if acc == nil {
			// A delta for a block we never saw start. Dropping it is the only
			// option that does not invent a block index.
			return nil
		}
		switch p.Delta.Type {
		case "text_delta":
			acc.text.WriteString(p.Delta.Text)
			d.s.Push(core.TextDeltaEvent{BlockIndex: p.Index, Delta: p.Delta.Text})
		case "thinking_delta":
			acc.thinking.WriteString(p.Delta.Thinking)
			d.s.Push(core.ThinkingDeltaEvent{BlockIndex: p.Index, Delta: p.Delta.Thinking})
		case "signature_delta":
			// A signature carries no incremental event: it is not content a UI
			// renders, and it arrives whole.
			acc.signature += p.Delta.Signature
		case "input_json_delta":
			acc.input = append(acc.input, p.Delta.PartialJSON...)
			d.s.Push(core.ToolInputDeltaEvent{BlockIndex: p.Index,
				ToolUseID: acc.id, Delta: p.Delta.PartialJSON})
		}

	case "content_block_stop":
		var p struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return fmt.Errorf("anthropic: content_block_stop: %w", err)
		}
		acc := d.accs[p.Index]
		if acc == nil {
			return nil
		}
		b := acc.block()
		if b == nil {
			return nil
		}
		if d.final == nil {
			d.final = map[int]core.ContentBlock{}
		}
		d.final[p.Index] = b
		if acc.salvaged {
			d.salvaged++
		}
		d.partial.Content = append(d.partial.Content, b)
		// A snapshot per completed block, so a diff-based renderer never needs
		// its own delta accumulator (REQ-OBS-06b).
		d.s.Push(core.MessageUpdateEvent{Message: d.partial})

	case "message_delta":
		var p struct {
			Delta struct {
				StopReason   string           `json:"stop_reason"`
				StopDetails  *wireStopDetails `json:"stop_details"`
				StopSequence *string          `json:"stop_sequence"`
			} `json:"delta"`
			Usage wireUsage `json:"usage"`
		}
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return fmt.Errorf("anthropic: message_delta: %w", err)
		}
		if p.Delta.StopReason != "" {
			d.stopRaw = p.Delta.StopReason
		}
		if p.Delta.StopDetails != nil {
			d.stopDetail = p.Delta.StopDetails.String()
		}
		if p.Delta.StopSequence != nil {
			d.stopSeq = *p.Delta.StopSequence
		}
		p.Usage.Into(&d.usage)

	case "message_stop":
		d.sawStop = true
	}
	return nil
}

// finish emits the authoritative phase and ends the stream.
func (d *decodeState) finish(m *core.Model, lookup func(string) *core.Model) {
	// Block-end events, in BLOCK ORDER, after the stream has fully ended
	// (REQ-OBS-08.3). Emitting them from the per-chunk handler produces
	// duplicate ends and a message end with no usage, because usage arrives
	// with the terminal chunk — which is exactly the event we have only now.
	for _, i := range d.order {
		acc, b := d.accs[i], d.final[i]
		if acc == nil || b == nil {
			continue
		}
		if e := acc.endEvent(i, b); e != nil {
			d.s.Push(e)
		}
	}

	final := d.partial
	final.StopReason = MapStopReason(d.stopRaw)
	final.RawStopReason = d.stopRaw
	final.StopDetail = d.stopDetail
	final.Usage = d.usage
	final.Usage.BilledModel = ""
	final.Usage.Requests = 1

	// REQ-PROV-05.5: bill the model that SERVED the request. Cost is computed
	// ONCE, here, from the final served name — never accumulated per event.
	// That is what makes "repriced back" fall out for free when a later event
	// names the requested model again.
	d.price(&final, m, lookup)

	d.s.Push(core.MessageEndEvent{Message: final})
	d.s.End(core.StreamResult{Message: &final})
}

// price bills a message against the model that served it. It is shared by
// the success and failure paths: a failed turn whose message_start already
// reported input tokens was billed for them, and a session aggregate that
// omits it under-reports by exactly the turns that went wrong.
func (d *decodeState) price(msg *core.AssistantMessage, m *core.Model, lookup func(string) *core.Model) {
	billModel, billed := provider.BillingModel(m, msg.ResponseModel, lookup)
	msg.Usage.BilledModel = billed
	if msg.Usage.Reported() {
		msg.Usage.SetCost(provider.ComputeCost(billModel, msg.Usage))
	}
}

// fail is REQ-PROV-04: the partial content and the failure are ONE value.
//
// Half an assistant message followed by a truncated stream is not an error
// with a message thrown away — the retry classifier reads the text and session
// persistence keeps the content, and neither works if the two are separated.
func (d *decodeState) fail(text string, err error) {
	final := d.partial
	final.Content = d.partialContent()
	final.Usage = d.usage
	final.Usage.BilledModel = ""
	final.Usage.Requests = 1
	d.price(&final, d.model, d.lookup)
	if text == AbortText {
		final.StopReason = core.StopReasonAborted
		final.ErrorMessage = text
		d.s.Push(core.MessageEndEvent{Message: final})
		d.s.End(core.StreamResult{Message: &final, Err: core.ErrAborted})
		return
	}
	final.StopReason = core.StopReasonError
	final.ErrorMessage = text
	d.s.Push(core.ErrorEvent{Message: text, Err: err, Terminal: true})
	d.s.Push(core.MessageEndEvent{Message: final})
	d.s.End(core.StreamResult{Message: &final, Err: err})
}

// partialContent is what a failed or aborted stream keeps: every completed
// block, then every block still open when the stream died, salvaged the same
// way the other wires' per-chunk snapshots keep theirs. REQ-LOOP-09 appends
// this message to history verbatim; the half-streamed text is what the UI and
// the session log display, so dropping an open block loses it twice.
func (d *decodeState) partialContent() core.Content {
	out := append(core.Content(nil), d.partial.Content...)
	for _, i := range d.order {
		if _, done := d.final[i]; done {
			continue
		}
		if acc := d.accs[i]; acc != nil {
			if b := acc.block(); b != nil {
				out = append(out, b)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- whole-response

// DecodeResponse decodes a NON-streaming Messages response.
//
// It exists for REQ-PROV-17's conformance test: the streaming and whole
// response paths must produce byte-identical ToolUseBlock.Input for the same
// call. Sharing the assembler is what makes that true by construction rather
// than by coincidence, and DecodeResponse is how the test can say so.
func DecodeResponse(m *core.Model, data []byte, lookup func(string) *core.Model) (*core.AssistantMessage, error) {
	if err := wire.Guard(data, wire.Limits{}); err != nil {
		return nil, fmt.Errorf("anthropic: decoding response: %w", err)
	}
	var wr wireResponse
	if err := json.Unmarshal(data, &wr); err != nil {
		return nil, fmt.Errorf("anthropic: decoding response: %w", err)
	}
	msg := &core.AssistantMessage{
		Model:      m.ID,
		ResponseID: wr.ID, ResponseModel: wr.Model,
		StopReason: MapStopReason(wr.StopReason), RawStopReason: wr.StopReason, StopDetail: wr.StopDetails.String(),
	}
	for _, raw := range wr.Content {
		acc, err := startFrom(raw, true)
		if err != nil {
			return nil, fmt.Errorf("anthropic: decoding response: %w", err)
		}
		if b := acc.block(); b != nil {
			msg.Content = append(msg.Content, b)
		}
	}
	wr.Usage.Into(&msg.Usage)
	msg.Usage.Requests = 1
	billModel, billed := provider.BillingModel(m, wr.Model, lookup)
	msg.Usage.BilledModel = billed
	if msg.Usage.Reported() {
		msg.Usage.SetCost(provider.ComputeCost(billModel, msg.Usage))
	}
	return msg, nil
}

// effortTokens is the output_config.effort vocabulary this adapter will send.
// "minimal" is not in it: no Anthropic model has that level, so a row that
// wants minimal to mean something maps it to "low" itself, and a request that
// reaches here as minimal is priced as low rather than rejected.
var effortTokens = map[string]string{
	"minimal": "low",
	"low":     "low",
	"medium":  "medium",
	"high":    "high",
	"xhigh":   "xhigh",
	"max":     "max",
}

// minThinkingBudget is the vendor minimum for budget_tokens; a smaller value
// is a 400.
const minThinkingBudget = 1024

// applyThinking encodes the requested effort for m (10-REQ-4):
//
//   - an adaptive model gets {"type":"adaptive"} and output_config.effort;
//   - a budget model gets {"type":"enabled","budget_tokens":N}, N being its
//     catalog row's budget for that effort;
//   - no effort, a model without thinking, or a level the row does not list
//     sends neither key. Nothing is clamped to another level.
//
// The row's level map may name a different wire effort for a level (an older
// row maps minimal to low); its value is what is sent.
func applyThinking(r *request, m *core.Model, effort core.Effort) {
	if effort == "" {
		return
	}
	wire, listed := string(effort), true
	if m.ThinkingLevelMap != nil {
		w := m.ThinkingLevelMap[core.ThinkingLevel(effort)]
		listed = w != nil
		if listed {
			wire = strings.ToLower(strings.TrimSpace(*w))
		}
	}
	if !listed {
		return
	}
	switch m.ThinkingMode() {
	case core.ThinkingKindBudget:
		if n, err := strconv.Atoi(wire); err == nil {
			applyBudget(r, n)
		}
	case core.ThinkingKindAdaptive:
		e, known := effortTokens[wire]
		if !known {
			// An effort the model has never heard of is a 400.
			return
		}
		r.Thinking = &thinking{Type: "adaptive"}
		r.OutputConfig = &outputConfig{Effort: e}
		// Thinking rejects any explicit temperature or top_p. Dropping them
		// is the only option that keeps the request valid.
		r.Temperature, r.TopP = nil, nil
	}
}

// applyBudget is the budget_tokens arm of applyThinking.
func applyBudget(r *request, n int) {
	// Anthropic rejects a thinking budget that is not strictly below
	// max_tokens. The budget is the value we may lower; max_tokens is the
	// caller's bound and lowering it would silently truncate the answer
	// instead.
	if n >= r.MaxTokens {
		n = r.MaxTokens - 1
	}
	if n < minThinkingBudget {
		// Below the vendor minimum — after the clamp, which is where this
		// happens in practice: a small max_tokens deep into a long session
		// leaves no room for the smallest legal budget. A sub-minimum budget
		// is a 400; no thinking is the answer that still returns.
		return
	}
	r.Thinking = &thinking{Type: "enabled", BudgetTokens: &n}
	r.Temperature, r.TopP = nil, nil
}
