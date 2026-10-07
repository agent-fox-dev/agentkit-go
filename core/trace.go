package core

// Span and Tracer live in core rather than in the root package because
// AgentConfig has to hold one: REQ-OBS-02 puts a span around every TOOL CALL,
// which happens in the batch executor, and a tracer reachable only through
// Axis 1 middleware can never see one.
//
// core holds declarations and interface seams; this is one.

type Span interface {
	SetAttributes(kv map[string]any)
	SetStatus(err error)
	AddEvent(name string, kv map[string]any)
	End()
}

// Tracer starts spans. StartSpan deliberately does NOT take a
// context.Context: cancellation belongs to the work the callback closes over,
// not to the tracing of it.
//
// THE SPAN LIVES UNTIL Span.End, not until fn returns. fn receives the span
// and returns once the span is set up; the caller writes attributes and calls
// End exactly once, and may do so after fn has returned — a model call's span
// is ended when its response completes, on another goroutine, so the stream's
// events are not held back until then. An implementation must therefore NOT
// end the span when fn returns (an OpenTelemetry adapter calls its span's End
// from Span.End, never from StartSpan), and the SDK never writes to a span
// after calling End. The tool-call span happens to end inside fn; nothing may
// rely on that.
type Tracer interface {
	StartSpan(name string, fn func(Span) error) error
}

type noopSpan struct{}

func (noopSpan) SetAttributes(map[string]any)    {}
func (noopSpan) SetStatus(error)                 {}
func (noopSpan) AddEvent(string, map[string]any) {}
func (noopSpan) End()                            {}

type noopTracer struct{}

func (noopTracer) StartSpan(_ string, fn func(Span) error) error { return fn(noopSpan{}) }

// NoopTracer is the shared, fieldless default. An untraced run neither
// inspects nor retains what it is handed.
var NoopTracer Tracer = noopTracer{}
