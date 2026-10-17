package mcpserve

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// endpointPath is the one path an endpoint serves.
	endpointPath = "/mcp"
	// tokenBytes is the size of a bearer token before it is hex-encoded.
	tokenBytes = 24
	// shutdownGrace is how long a stopping endpoint lets its requests finish
	// before it closes their connections.
	shutdownGrace = 2 * time.Second
)

// The listener, the random source, the SDK's HTTP handler and the header
// timeout are seams: a test replaces them to fail a listen or the token, to see
// whether a refused request ever reached the SDK, and to shorten the wait for
// a client's headers. Production code never assigns them.
var (
	// headerTimeout bounds how long a client may take to send its headers.
	headerTimeout            = 10 * time.Second
	listenLoopback           = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	randomSource   io.Reader = rand.Reader
	newSDKHandler            = func(srv *sdk.Server) http.Handler {
		return sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	}
)

// ServeLoopback serves the tools over Streamable HTTP on a loopback port, for a
// harness on this machine, and returns where it is and the token it needs.
//
// It listens on 127.0.0.1 on a port the system chooses, with a token generated
// for this call. Every request must carry "Authorization: Bearer <token>",
// compared in constant time, or it is refused with 401 before its body is read;
// only /mcp is served; and a request body is read up to wire.Defaults()'s message
// size (413 above it) and must pass wire.Guard (400 if it does not) before the
// SDK's handler, with its default stateful sessions, sees it.
//
// The endpoint serves until Endpoint.Close is called or ctx ends, whichever
// comes first. It then stops accepting connections, lets requests in flight
// finish for up to two seconds, and closes what remains, which cancels the
// calls still running: they see a cancelled context. A client's standalone
// event stream never ends on its own, which is why the wait is bounded.
//
// A ctx that is already done returns nil and ctx.Err() and listens on nothing.
func (s *Server) ServeLoopback(ctx context.Context) (*Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := listenLoopback()
	if err != nil {
		return nil, fmt.Errorf("mcpserve: listening on loopback: %w", err)
	}
	token, err := newToken()
	if err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("mcpserve: generating a token: %w", err)
	}
	// life ends when the endpoint is force-closed. The SDK cancels a call only
	// when its client asks, so each request carries life for the call handler
	// to obey (see endWithLifetime).
	life, end := context.WithCancel(context.Background())
	guarded := s.guarded(token, newSDKHandler(s.srv))
	hs := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guarded.ServeHTTP(w, r.WithContext(withLifetime(r.Context(), life)))
		}),
		ReadHeaderTimeout: headerTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(serverErrors{s.log}, "", 0),
	}
	go func() { _ = hs.Serve(l) }()

	var once sync.Once
	stopped := make(chan struct{})
	stop := func() {
		once.Do(func() {
			defer close(stopped)
			defer end()
			grace, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			if hs.Shutdown(grace) != nil {
				end()
				_ = hs.Close()
			}
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-stopped:
		}
	}()
	return &Endpoint{
		URL:   "http://" + l.Addr().String() + endpointPath,
		Token: token,
		stop:  stop,
	}, nil
}

// serverErrors receives what net/http reports about its own failures, such as a
// handler that panicked. By default that goes to the standard logger, which is
// the process's stderr, with the panic's text; here it becomes a Warn record on
// the Logger with a fixed message, because the text is workspace content.
type serverErrors struct{ log *slog.Logger }

func (w serverErrors) Write(p []byte) (int, error) {
	w.log.Warn("http server error")
	return len(p), nil
}

// newToken is tokenBytes bytes of randomness as lowercase hex.
func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := io.ReadFull(randomSource, raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// guarded is the endpoint's trust boundary, in front of the SDK's handler. The
// order matters: an unauthenticated peer learns nothing about the server and
// makes it read no body, then only /mcp is served, and only then is the body
// read, bounded and checked by wire.Guard, so the SDK's decoder, which accepts
// a duplicate key, never sees a body wire would refuse.
func (s *Server) guarded(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	limits := wire.Defaults()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Values("Authorization"); len(got) != 1 || subtle.ConstantTimeCompare([]byte(got[0]), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			s.refuse(w, r, http.StatusUnauthorized, "unauthorized", "missing or wrong token")
			return
		}
		if r.URL.Path != endpointPath {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limits.MaxMessageBytes))
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			s.refuse(w, r, http.StatusRequestEntityTooLarge, "request body too large", "body over the size limit")
			return
		case err != nil:
			s.refuse(w, r, http.StatusBadRequest, "bad request", "body unreadable")
			return
		}
		if len(body) > 0 {
			if err := wire.Guard(body, limits); err != nil {
				reason := "body rejected"
				var we *wire.Error
				if errors.As(err, &we) {
					reason += ": " + string(we.Rule)
				}
				s.refuse(w, r, http.StatusBadRequest, "bad request", reason)
				return
			}
		}
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		next.ServeHTTP(w, r)
	})
}

// refuse answers a request with status and text, and logs one Warn record
// naming the peer and why. The record never carries a header or any of the
// body.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, text, reason string) {
	s.log.Warn("request refused", "peer", r.RemoteAddr, "reason", reason)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}
