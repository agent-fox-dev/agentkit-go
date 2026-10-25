package gatewayd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

type ctxStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s ctxStream) Context() context.Context { return s.ctx }

// An interceptor meeting a context without a principal (unreachable from outside: the tap sets it) refuses with reason "refused".
func TestCallbackInterceptorsRefuseAContextWithoutAPrincipal(t *testing.T) {
	var logs bytes.Buffer
	calls, ran := 0, false
	s, err := New(Config{Backend: local.Unisolated(), NetworkModes: []sandbox.NetworkMode{sandbox.NetworkUnrestricted},
		Authenticate: func(context.Context, string) (string, bool) { calls++; return "p", true },
		Logger:       slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_, uerr := s.unaryAuth(context.Background(), nil, nil, func(context.Context, any) (any, error) { ran = true; return nil, nil })
	serr := s.streamAuth(nil, ctxStream{ctx: context.Background()}, nil, func(any, grpc.ServerStream) error { ran = true; return nil })
	if status.Code(uerr) != codes.Unauthenticated || status.Code(serr) != codes.Unauthenticated || ran || calls != 0 {
		t.Errorf("unary %v, stream %v, a handler ran %v, %d authenticator calls; want both refused and nothing run", uerr, serr, ran, calls)
	}
	if n := strings.Count(logs.String(), `"reason":"refused"`); n != 2 {
		t.Errorf("%d refusals with reason refused, want 2:\n%s", n, logs.String())
	}
}
