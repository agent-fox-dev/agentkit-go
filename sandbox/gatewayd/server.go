package gatewayd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Serve serves the gateway on l until Shutdown: TLS when Config.TLS was set,
// plaintext otherwise, with sandbox.MaxMessageBytes as the message limits
// and every RPC authenticated. It runs Backend.Reap once at start and then
// every ReapInterval; a failed reap is logged, never fatal. It returns nil
// after Shutdown and the listener's error otherwise. Serve is called once.
func (s *Server) Serve(l net.Listener) error {
	first := false
	s.serveOnce.Do(func() { first = true })
	if !first {
		return errors.New("gatewayd: Serve was already called")
	}
	select {
	case <-s.stopped:
		_ = l.Close()
		return nil
	default:
	}
	go s.reapLoop()
	err := s.srv.Serve(l)
	select {
	case <-s.stopped:
		return nil
	default:
		return err
	}
}

// reapLoop runs Backend.Reap now and every reapInterval until Shutdown.
func (s *Server) reapLoop() {
	t := time.NewTicker(s.reapInterval)
	defer t.Stop()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), s.reapInterval)
		_, _ = s.reapBackend(ctx)
		cancel()
		select {
		case <-t.C:
		case <-s.stopped:
			return
		}
	}
}

// reapBackend runs Backend.Reap once and logs the outcome.
func (s *Server) reapBackend(ctx context.Context) ([]string, error) {
	ids, err := s.backend.Reap(ctx)
	s.logReap(ids, err)
	return ids, err
}

// ErrNotHeld is what Destroy returns, wrapped with the id, for a sandbox the
// server does not hold: an id it never held, one already removed by any path
// (a client Destroy, expiry, unavailability, Shutdown or an earlier Destroy),
// one whose Create has not been recorded yet, and any id before Serve or
// after Shutdown. An embedder that wants Destroy to be idempotent treats
// errors.Is(err, ErrNotHeld) as "already gone".
var ErrNotHeld = errors.New("gatewayd: sandbox not held")

// Destroy ends the sandbox the server holds under id, whoever created it,
// with the effects of an expiry: the entry is removed and its timer stopped,
// the backend sandbox's Close is called with ctx (which ends the relayed
// calls in flight) and "sandbox destroyed" is logged with the reason
// "embedder". The owner's next proxy call then gets sandbox_unavailable, its
// List omits the id, its Destroy answers OK and its SyncGit answers
// Unavailable.
//
// It returns the Close error wrapped with the id, so errors.Is and errors.As
// reach the backend's error. The entry stays removed even if Close fails or
// ctx is cancelled. For an id the server does not hold it calls no backend
// method and returns an error that matches ErrNotHeld and names the id. If a
// concurrent removal of the same entry wins after the lookup, Destroy returns
// nil: the backend sandbox is closed once, by the removal that won.
//
// A Create still in progress is not held. The Backend sees the new sandbox's
// id before the server records it, and a Destroy in that window returns
// ErrNotHeld; the embedder then holds the backend sandbox and can close it
// itself.
func (s *Server) Destroy(ctx context.Context, id string) error {
	e := s.reg.get(id)
	if e == nil {
		return fmt.Errorf("%w: %q", ErrNotHeld, id)
	}
	if err := s.reg.remove(ctx, e, reasonEmbedder); err != nil {
		return fmt.Errorf("gatewayd: destroy %q: %w", id, err)
	}
	return nil
}

// Shutdown stops the gateway: it stops accepting RPCs, closes every backend
// sandbox it holds (which ends the relayed calls in flight), waits for the
// handlers until ctx is done, and then forces the server to stop. It
// returns the joined Close errors, and nil on later calls.
func (s *Server) Shutdown(ctx context.Context) error {
	first := false
	s.shutdownOnce.Do(func() {
		first = true
		close(s.stopped)
		done := make(chan struct{})
		go func() {
			s.srv.GracefulStop()
			close(done)
		}()
		// Closing every sandbox ends the relayed calls GracefulStop waits for.
		s.shutdownErr = s.reg.closeAll(reasonShutdown)
		select {
		case <-done:
		case <-ctx.Done():
			s.srv.Stop()
			<-done
		}
	})
	if !first {
		return nil
	}
	return s.shutdownErr
}
