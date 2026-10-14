package gatewayd

import (
	"context"
	"errors"
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
