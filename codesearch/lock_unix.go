//go:build !windows

package codesearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockPoll is how often a waiter tries the store lock again.
const lockPoll = 25 * time.Millisecond

// lockStore takes the advisory lock of the workspace's store directory, which
// one process holds while it loads or builds and stores the index. The kernel
// releases it when its holder exits, however it exits. A waiter waits for it,
// abandonably, up to 1.25 times MaxBuildTime plus 5 seconds (the longest a
// build runs, and a margin for its store); after that held is false and the
// caller builds for itself without storing, so a stuck builder cannot stall
// it. A store directory that cannot be created or opened also gives held
// false. err is non-nil only when ctx ended while waiting.
func (idx *Index) lockStore(ctx context.Context) (unlock func(), held bool, err error) {
	storeDir := idx.storeDirPath()
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, false, nil
	}
	f, err := os.OpenFile(filepath.Join(storeDir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, nil
	}
	wait := idx.lockWait
	if wait <= 0 {
		wait = idx.opts.MaxBuildTime*5/4 + 5*time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, true, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, false, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, false, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
