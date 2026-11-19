package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// withSignals is ctx, cancelled when SIGINT or SIGTERM arrives, with the exit
// status that signal asks for (130 or 143, 0 while none has) and the function
// that stops listening.
func withSignals(ctx context.Context) (context.Context, func() int, func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	var status atomic.Int32
	go func() {
		select {
		case sig := <-ch:
			if sig == syscall.SIGTERM {
				status.Store(143)
			} else {
				status.Store(130)
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() int { return int(status.Load()) }, func() {
		signal.Stop(ch)
		cancel()
	}
}
