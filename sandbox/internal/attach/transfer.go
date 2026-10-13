package attach

import (
	"context"
	"errors"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// Sync streams a start chunk and then the tar in data chunks of at most
// tarsafe.ChunkBytes; the agent extracts it as it arrives. A refused entry
// is ErrUnsafeArchive. A read error from the tar or a done ctx cancels the
// stream and is returned. None of these changes the sandbox's state; a
// transport failure does.
func (s *sb) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	if err := s.gate(); err != nil {
		return sandbox.SyncResult{}, err
	}
	if ld, ok := src.(sandbox.LocalDir); ok {
		return s.syncLocalDir(ctx, ld)
	}
	t, ok := src.(sandbox.Tar)
	if !ok || t.R == nil {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: unsupported source %T or a Tar with no reader", src)
	}
	rel, err := tarsafe.CleanRel(t.Path)
	if err != nil {
		return sandbox.SyncResult{}, fmt.Errorf("sandbox: sync: %w", err)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.client.SyncTar(s.outgoing(sctx))
	if err != nil {
		return sandbox.SyncResult{}, s.classify(ctx, "SyncTar", err)
	}
	var sent int64
	if err := stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Start{Start: &sandboxpb.TarStart{Path: rel, Replace: t.Replace}}}); err == nil {
		if sent, err = sendChunks(ctx, stream, readChunks(sctx, t.R)); err != nil {
			return sandbox.SyncResult{}, err
		}
	}
	resp, err := stream.CloseAndRecv()
	if status.Code(err) == codes.InvalidArgument {
		return sandbox.SyncResult{}, fmt.Errorf("%w: %s", sandbox.ErrUnsafeArchive, status.Convert(err).Message())
	}
	if err != nil {
		return sandbox.SyncResult{}, s.classify(ctx, "SyncTar", err)
	}
	return sandbox.SyncResult{Files: int(resp.Files), Bytes: resp.Bytes, Sent: sent}, nil
}

// chunk is one read from a tar source.
type chunk struct {
	data []byte
	err  error
}

// readChunks reads r on its own goroutine, so a read that never returns
// cannot hold Sync past its context, and delivers reads of at most
// tarsafe.ChunkBytes until an error, or until ctx ends.
func readChunks(ctx context.Context, r io.Reader) <-chan chunk {
	out := make(chan chunk)
	go func() {
		for {
			buf := make([]byte, tarsafe.ChunkBytes)
			n, err := r.Read(buf)
			select {
			case out <- chunk{buf[:n], err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return out
}

// sendChunks sends each read as one data chunk until the source ends, and
// returns the bytes sent. It returns the read error or ctx's error; a send
// that fails ends the loop quietly, since CloseAndRecv then reports why the
// stream ended.
func sendChunks(ctx context.Context, stream sandboxpb.Agent_SyncTarClient, reads <-chan chunk) (int64, error) {
	var sent int64
	for {
		select {
		case <-ctx.Done():
			return sent, fmt.Errorf("sandbox: sync: %w", ctx.Err())
		case c := <-reads:
			if len(c.data) > 0 {
				if err := stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: c.data}}); err != nil {
					return sent, nil
				}
				sent += int64(len(c.data))
			}
			if c.err == io.EOF {
				return sent, nil
			}
			if c.err != nil {
				return sent, fmt.Errorf("sandbox: sync: reading the tar: %w", c.err)
			}
		}
	}
}

// Export receives the agent's tar of path, treats it as untrusted, and
// writes each entry the rules allow to the destination. The first refused
// entry stops it with ErrUnsafeArchive, writing nothing from that entry on;
// a path that does not exist is an error naming it. Neither changes the
// sandbox's state; a transport failure does.
func (s *sb) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	if err := s.gate(); err != nil {
		return err
	}
	if ld, ok := dst.(sandbox.LocalDir); ok {
		return s.exportLocalDir(ctx, path, ld)
	}
	out, ok := dst.(sandbox.TarOut)
	if !ok {
		return fmt.Errorf("sandbox: unsupported destination %T", dst)
	}
	rel, err := tarsafe.CleanRel(path)
	if err != nil {
		return fmt.Errorf("sandbox: export: %w", err)
	}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := s.client.ExportTar(s.outgoing(sctx), &sandboxpb.ExportRequest{Path: rel})
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
	_ = pr.Close()
	var ee *tarsafe.EntryError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ee):
		return fmt.Errorf("%w: %v", sandbox.ErrUnsafeArchive, ee)
	case status.Code(err) == codes.NotFound:
		return fmt.Errorf("sandbox: export: path %q does not exist in the workspace", path)
	}
	if _, isStatus := status.FromError(err); !isStatus && ctx.Err() == nil {
		// Not from the stream: the destination failed, which is the
		// caller's error and says nothing about the sandbox.
		return fmt.Errorf("sandbox: export: %w", err)
	}
	return s.classify(ctx, "ExportTar", err)
}
