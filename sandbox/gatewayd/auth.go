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
// It reports ok == false on the context of the periodic reap, on the context
// of every Close the server makes on its own, and on any context the server
// did not make. There is no exported way to put a principal on a context.
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

// errUnauthenticated is every refused RPC's answer.
var errUnauthenticated = status.Error(codes.Unauthenticated, "gatewayd: a valid bearer token is required")

// authenticate is the principal of ctx's `authorization: Bearer <token>`, or
// the reason it is refused.
func (s *Server) authenticate(ctx context.Context) (principal, reason string) {
	md, _ := metadata.FromIncomingContext(ctx)
	return s.authenticateMD(md)
}

// authenticateMD is authenticate over the request's header metadata: the
// principal of the single `authorization: Bearer <token>` it holds, or the
// reason the RPC is refused (a header that is missing, repeated, not Bearer or
// empty, or a token no configured token matches). Every configured token is
// compared, in constant time, whatever the outcome.
func (s *Server) authenticateMD(md metadata.MD) (principal, reason string) {
	vals := md.Get("authorization")
	if len(vals) != 1 {
		return "", reasonNoToken
	}
	tok, ok := strings.CutPrefix(vals[0], "Bearer ")
	if !ok || tok == "" {
		return "", reasonNoToken
	}
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
// also covers a method no handler serves.
func (s *Server) tap(ctx context.Context, info *tap.Info) (context.Context, error) {
	p, reason := s.authenticateMD(info.Header)
	if reason != "" {
		return nil, s.refuse(ctx, reason)
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}

// unaryAuth authenticates a unary RPC again before its handler runs.
func (s *Server) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	p, reason := s.authenticate(ctx)
	if reason != "" {
		return nil, s.refuse(ctx, reason)
	}
	return h(context.WithValue(ctx, principalKey{}, p), req)
}

// streamAuth authenticates a streaming RPC again before its handler reads a
// frame.
func (s *Server) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	p, reason := s.authenticate(ss.Context())
	if reason != "" {
		return s.refuse(ss.Context(), reason)
	}
	return h(srv, ownedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), principalKey{}, p)})
}

// ownedStream is a ServerStream whose context carries the principal.
type ownedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ownedStream) Context() context.Context { return s.ctx }
