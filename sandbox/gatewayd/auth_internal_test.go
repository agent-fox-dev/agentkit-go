package gatewayd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// ctxStream is a ServerStream that has only a context.
type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

// TS-25-13: a callback-mode interceptor given a context without a principal
// refuses with Unauthenticated and logs reason "refused". 25-REQ-3.5,
// 25-REQ-6.4. It is an in-package test, because it calls the interceptors
// directly, and so it cannot use gwtest, which imports this package.
func TestInterceptorsRefuseWithoutPrincipal_TS25_13(t *testing.T) {
	var logs bytes.Buffer
	var calls atomic.Int32
	s, err := New(Config{
		Backend:      local.Unisolated(),
		NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		Authenticate: func(context.Context, string) (string, bool) { calls.Add(1); return "p", true },
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	_, uerr := s.unaryAuth(context.Background(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		ran = true
		return nil, nil
	})
	serr := s.streamAuth(nil, ctxStream{ctx: context.Background()}, &grpc.StreamServerInfo{}, func(any, grpc.ServerStream) error {
		ran = true
		return nil
	})
	for name, err := range map[string]error{"unary": uerr, "stream": serr} {
		if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != "gatewayd: a valid bearer token is required" {
			t.Errorf("%s interceptor: %v, want Unauthenticated \"gatewayd: a valid bearer token is required\"", name, err)
		}
	}
	if ran {
		t.Error("a handler ran for a context without a principal")
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the authenticator was called %d times, want 0", n)
	}
	var refused int
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil || rec["msg"] != "authentication refused" {
			continue
		}
		refused++
		if rec["reason"] != "refused" {
			t.Errorf("the refusal record has reason %v, want \"refused\"", rec["reason"])
		}
	}
	if refused != 2 {
		t.Errorf("%d authentication refused records, want 2: %s", refused, logs.String())
	}
}
