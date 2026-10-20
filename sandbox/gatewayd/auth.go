package gatewayd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
)

// principalKey carries an RPC's principal in its context. It is unexported,
// so only the server puts a principal on a context.
type principalKey struct{}

// PrincipalFromContext is the principal of the authenticated RPC that ctx was
// made for: in static mode the 64-character lowercase hex SHA-256 of its
// token, in callback mode what the Authenticator returned. A Backend reads it
// in Create, in the Sync of a sandbox that a SyncGit RPC syncs, and in Reap
// when the Gateway.Reap RPC calls it, to learn which caller it serves.
//
// A principal is not a secret. PrincipalFromContext reports ok == false on
// the context of the periodic reap, on the context of every Close the server
// makes on its own, and on any context the server did not make. There is no
// exported way to put a principal on a context.
func PrincipalFromContext(ctx context.Context) (principal string, ok bool) {
	principal, ok = ctx.Value(principalKey{}).(string)
	return principal, ok
}

// ownerOf is the owner of the sandboxes of the RPC that ctx was made for: its
// principal.
func ownerOf(ctx context.Context) string {
	p, _ := PrincipalFromContext(ctx)
	return p
}

// maxPrincipalLen is the longest principal an Authenticator may return.
const maxPrincipalLen = 256

// errUnauthenticated is every refused RPC's answer.
var errUnauthenticated = status.Error(codes.Unauthenticated, "gatewayd: a valid bearer token is required")

// bearer is the token of md's single `authorization: Bearer <token>`, or
// false when the header is missing, repeated, not Bearer or empty.
func bearer(md metadata.MD) (string, bool) {
	vals := md.Get("authorization")
	if len(vals) != 1 {
		return "", false
	}
	tok, ok := strings.CutPrefix(vals[0], "Bearer ")
	return tok, ok && tok != ""
}

// authenticate is the principal of ctx's `authorization: Bearer <token>`, or
// the reason it is refused.
func (s *Server) authenticate(ctx context.Context) (principal, reason string) {
	md, _ := metadata.FromIncomingContext(ctx)
	return s.authenticateMD(ctx, md)
}

// authenticateMD is authenticate over the request's header metadata, with ctx
// for the Authenticator in callback mode: the principal of the token, or the
// reason the RPC is refused.
func (s *Server) authenticateMD(ctx context.Context, md metadata.MD) (principal, reason string) {
	tok, ok := bearer(md)
	if !ok {
		return "", reasonNoToken
	}
	if s.authenticator != nil {
		return s.callAuthenticator(ctx, tok)
	}
	return s.matchToken(tok)
}

// matchToken is static mode's authentication: the hex SHA-256 of tok when it
// matches a configured token. Every configured token is compared, in constant
// time, whatever the outcome.
func (s *Server) matchToken(tok string) (principal, reason string) {
	sum := sha256.Sum256([]byte(tok))
	match := 0
	for _, o := range s.tokenSums {
		match |= subtle.ConstantTimeCompare(sum[:], o[:])
	}
	if match != 1 {
		return "", reasonRefused
	}
	return hex.EncodeToString(sum[:]), ""
}

// callAuthenticator runs the embedder's Authenticator on tok and checks the
// principal it returns. A panic is recovered and reported without its value,
// which could hold the token. Whether the Authenticator returned decides it,
// not what recover returns: recover returns nil for panic(nil) under
// GODEBUG=panicnil=1, and a check on that would let the RPC in.
func (s *Server) callAuthenticator(ctx context.Context, tok string) (principal, reason string) {
	returned := false
	defer func() {
		if !returned {
			_ = recover() // the value could hold the token: it is dropped, never logged
			principal, reason = "", reasonPanic
		}
	}()
	p, ok := s.authenticator(ctx, tok)
	returned = true
	switch {
	case !ok:
		return "", reasonRefused
	case p == "" || len(p) > maxPrincipalLen:
		return "", reasonInvalidPrincipal
	}
	return p, ""
}

// refuse logs a refused authentication with the peer's address and reason.
func (s *Server) refuse(ctx context.Context, reason string) error {
	addr := "unknown"
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		addr = p.Addr.String()
	}
	s.logAuthRefused(addr, reason)
	return errUnauthenticated
}

// tap authenticates every RPC from its headers alone, before gRPC reads a
// message: a unary handler decodes its request before any interceptor runs,
// so a refusal here is what keeps an unauthenticated request unread. It
// also covers a method no handler serves. In callback mode it is the only
// place the Authenticator runs, once for each RPC, with the context that
// carries the peer address.
func (s *Server) tap(ctx context.Context, info *tap.Info) (context.Context, error) {
	p, reason := s.authenticateMD(ctx, info.Header)
	if reason != "" {
		return nil, s.refuse(ctx, reason)
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// admit is ctx carrying the principal of the RPC it was made for, or the
// refusal. Static mode authenticates a second time, from the headers. In
// callback mode the tap has already run the Authenticator, so admit only
// checks that ctx carries its principal: a second call could answer
// differently if the token was revoked in between.
func (s *Server) admit(ctx context.Context) (context.Context, error) {
	if s.authenticator != nil {
		if _, ok := PrincipalFromContext(ctx); !ok {
			return nil, s.refuse(ctx, reasonRefused)
		}
		return ctx, nil
	}
	p, reason := s.authenticate(ctx)
	if reason != "" {
		return nil, s.refuse(ctx, reason)
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// unaryAuth admits a unary RPC again before its handler runs.
func (s *Server) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	ctx, err := s.admit(ctx)
	if err != nil {
		return nil, err
	}
	return h(ctx, req)
}

// streamAuth admits a streaming RPC again before its handler reads a frame.
func (s *Server) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	ctx, err := s.admit(ss.Context())
	if err != nil {
		return err
	}
	return h(srv, ownedStream{ServerStream: ss, ctx: ctx})
}

// ownedStream is a ServerStream whose context carries the principal.
type ownedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ownedStream) Context() context.Context { return s.ctx }
