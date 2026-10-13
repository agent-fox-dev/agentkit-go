package agent

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// SyncTar extracts a tar stream into the workspace as the chunks arrive.
// The first chunk names the target path and whether to replace its
// contents. A refused or malformed entry ends the sync with InvalidArgument
// naming it; entries extracted before it stay. After a successful sync a
// configured tool set is rebuilt.
func (a *Agent) SyncTar(stream sandboxpb.Agent_SyncTarServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "the first chunk must be a start chunk")
	}
	rel, err := tarsafe.CleanRel(start.Path)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	target, err := a.prepareTarget(rel, start.Replace)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "sync target %q: %v", start.Path, err)
	}
	defer target.Close()

	pr, pw := io.Pipe()
	go func() {
		for {
			c, err := stream.Recv()
			if err == io.EOF {
				pw.Close()
				return
			}
			if err == nil && c.GetData() == nil {
				err = status.Error(codes.InvalidArgument, "a chunk after the first must carry data")
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(c.GetData()); err != nil {
				return
			}
		}
	}()
	st, err := tarsafe.Extract(target, pr)
	if err != nil {
		pr.CloseWithError(err)
		var ee *tarsafe.EntryError
		if errors.As(err, &ee) {
			return status.Error(codes.InvalidArgument, ee.Error())
		}
		return err
	}
	// Read what follows the end-of-archive marker, so the host's sends
	// complete and a stream error after it is not lost.
	if _, err := io.Copy(io.Discard, pr); err != nil {
		return err
	}
	a.rebuild()
	return stream.SendAndClose(&sandboxpb.SyncResponse{Files: int64(st.Files), Bytes: st.Bytes})
}

// prepareTarget creates rel under the workspace, empties it when replace is
// set, and opens it as a root.
func (a *Agent) prepareTarget(rel string, replace bool) (*os.Root, error) {
	ws, err := os.OpenRoot(a.root)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	if err := ws.MkdirAll(rel, 0o755); err != nil {
		return nil, err
	}
	if replace {
		ents, err := fs.ReadDir(ws.FS(), rel)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if err := ws.RemoveAll(path.Join(rel, e.Name())); err != nil {
				return nil, err
			}
		}
	}
	return ws.OpenRoot(rel)
}

// ExportTar streams a tar of a workspace subtree in chunks of at most
// tarsafe.ChunkBytes. A path that does not exist is NotFound.
func (a *Agent) ExportTar(req *sandboxpb.ExportRequest, stream sandboxpb.Agent_ExportTarServer) error {
	rel, err := tarsafe.CleanRel(req.Path)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	ws, err := os.OpenRoot(a.root)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "workspace: %v", err)
	}
	defer ws.Close()
	if _, err := ws.Lstat(rel); errors.Is(err, fs.ErrNotExist) {
		return status.Errorf(codes.NotFound, "export path %q does not exist", req.Path)
	} else if err != nil {
		return status.Errorf(codes.InvalidArgument, "export path %q: %v", req.Path, err)
	}
	sent := &chunkSender{stream: stream}
	w := bufio.NewWriterSize(sent, tarsafe.ChunkBytes)
	if err := tarsafe.Archive(ws, rel, w); err != nil {
		if sent.err != nil {
			return sent.err
		}
		return status.Errorf(codes.FailedPrecondition, "export %q: %v", req.Path, err)
	}
	return w.Flush()
}

// chunkSender sends what is written to it as data chunks of at most
// tarsafe.ChunkBytes.
type chunkSender struct {
	stream sandboxpb.Agent_ExportTarServer
	err    error
}

func (s *chunkSender) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := p[:min(len(p), tarsafe.ChunkBytes)]
		if s.err = s.stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: c}}); s.err != nil {
			return n, s.err
		}
		n += len(c)
		p = p[len(c):]
	}
	return n, nil
}
