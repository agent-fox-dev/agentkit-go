package ollama

import "net/http"

// attempt is the side channel of one Chat call. The transport records in it
// what Client.Chat leaves out: Chat returns nil, not an error, for an error
// status whose body is empty, and the provider needs the status to tell that
// from a stream that was cut.
type attempt struct {
	status int
}

type attemptKey struct{}

// credentialFree is the RoundTripper every request goes through. It deletes an
// Authorization header before the request goes on to the embedder's transport,
// so no credential is sent whatever the SDK adds (it signs a request when
// OLLAMA_AUTH is set in the process environment). The embedder's transport runs
// beneath it and may add headers of its own, an Authorization for a proxy in
// front of Ollama included. It also records the status of the response in the
// request's attempt.
type credentialFree struct{ inner http.RoundTripper }

func (t credentialFree) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	resp, err := t.inner.RoundTrip(req)
	if a, ok := req.Context().Value(attemptKey{}).(*attempt); ok && err == nil {
		a.status = resp.StatusCode
	}
	return resp, err
}

// sdkHTTPClient returns the client the SDK sends through: a copy of the
// embedder's, so its timeout, redirect policy and cookie jar are kept, or a new
// one, with its transport (the default transport when it has none) behind the
// credentialFree layer.
func sdkHTTPClient(embedder *http.Client) *http.Client {
	var hc http.Client
	if embedder != nil {
		hc = *embedder
	}
	inner := hc.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	hc.Transport = credentialFree{inner: inner}
	return &hc
}
