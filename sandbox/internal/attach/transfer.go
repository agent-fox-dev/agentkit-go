package attach

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// Sync streams a start chunk and then the tar in data chunks of at most
// tarsafe.ChunkBytes; the agent extracts it as it arrives.
func (s *sb) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	if err := s.gate(); err != nil {
		return sandbox.SyncResult{}, err
	}
	t, ok := src.(sandbox.Tar)
	if !ok {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: unsupported source %T", src)
	}
	rel, err := tarsafe.CleanRel(t.Path)
	if err != nil {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: sync: %w", err)
	}
	stream, err := s.client.SyncTar(s.outgoing(ctx))
	if err != nil {
		return sandbox.SyncResult{}, s.classify(ctx, "SyncTar", err)
	}
	if err := stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Start{Start: &sandboxpb.TarStart{Path: rel, Replace: t.Replace}}}); err == nil {
		buf := make([]byte, tarsafe.ChunkBytes)
		for {
			n, rerr := t.R.Read(buf)
			if n > 0 {
				if err := stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: buf[:n]}}); err != nil {
					break
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				return sandbox.SyncResult{}, fmt.Errorf("sandbox: sync: reading the tar: %w", rerr)
			}
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return sandbox.SyncResult{}, s.classify(ctx, "SyncTar", err)
	}
	return sandbox.SyncResult{Files: int(resp.Files), Bytes: resp.Bytes}, nil
}

// Export receives the agent's tar of path and writes each entry the rules
// allow to the destination.
func (s *sb) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	if err := s.gate(); err != nil {
		return err
	}
	out, ok := dst.(sandbox.TarOut)
	if !ok {
		return fmt.Errorf("sandbox: unsupported destination %T", dst)
	}
	stream, err := s.client.ExportTar(s.outgoing(ctx), &sandboxpb.ExportRequest{Path: path})
	if err != nil {
		return s.classify(ctx, "ExportTar", err)
	}
	pr, pw := io.Pipe()
	go func() {
		for {
			c, err := stream.Recv()
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(c.GetData()); err != nil {
				return
			}
		}
	}()
	err = tarsafe.ValidateStream(pr, out.W)
	pr.Close()
	var ee *tarsafe.EntryError
	if err != nil && !errors.As(err, &ee) && !errors.Is(err, io.EOF) {
		return s.classify(ctx, "ExportTar", err)
	}
	return err
}
