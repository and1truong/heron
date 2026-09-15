//go:build darwin || linux

package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// The OS releases this lock if the CLI crashes. Never unlink the lock inode.
func (i Instance) Lock(ctx context.Context) (func(), error) {
	if err := i.Prepare(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(i.Dir, "control.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
