package gateway

import "context"

// bearer sends the token as per-RPC credentials. For an https gateway it
// requires transport security, so gRPC never sends it in clear text.
type bearer struct {
	token      string
	requireTLS bool
}

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

func (b bearer) RequireTransportSecurity() bool { return b.requireTLS }
