package gatewayd

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// owner is the SHA-256 of the token that authenticated an RPC: the only
// identity the server keeps.
type owner [sha256.Size]byte

// ownerKey carries an RPC's owner in its context.
type ownerKey struct{}

// ownerOf is the owner the authentication interceptor put in ctx.
func ownerOf(ctx context.Context) owner {
	o, _ := ctx.Value(ownerKey{}).(owner)
	return o
}

// errUnauthenticated is every refused RPC's answer.
var errUnauthenticated = status.Error(codes.Unauthenticated, "gatewayd: a valid bearer token is required")

// authenticate is the owner of ctx's `authorization: Bearer <token>`, or
// false when it is missing, malformed or matches no configured token. Every
// configured token is compared, in constant time, whatever the outcome.
func (s *Server) authenticate(ctx context.Context) (owner, bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization")
	if len(vals) != 1 {
		return owner{}, false
	}
	tok, ok := strings.CutPrefix(vals[0], "Bearer ")
	if !ok || tok == "" {
		return owner{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	match := 0
	for _, o := range s.owners {
		match |= subtle.ConstantTimeCompare(sum[:], o[:])
	}
	return sum, match == 1
}

// refuse logs a refused authentication with the peer's address.
func (s *Server) refuse(ctx context.Context) error {
	addr := "unknown"
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		addr = p.Addr.String()
	}
	s.logAuthRefused(addr)
	return errUnauthenticated
}

// unaryAuth authenticates a unary RPC before its handler reads anything.
func (s *Server) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	o, ok := s.authenticate(ctx)
	if !ok {
		return nil, s.refuse(ctx)
	}
	return h(context.WithValue(ctx, ownerKey{}, o), req)
}

// streamAuth authenticates a streaming RPC before its handler reads a frame.
func (s *Server) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	o, ok := s.authenticate(ss.Context())
	if !ok {
		return s.refuse(ss.Context())
	}
	return h(srv, ownedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), ownerKey{}, o)})
}

// ownedStream is a ServerStream whose context carries the owner.
type ownedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ownedStream) Context() context.Context { return s.ctx }
