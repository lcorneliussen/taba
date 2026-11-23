package wtc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"taba.sh/taba/internal/retry"
)

const statusWatcherDir = ".wtc-status-watchers"

// Retirement stops leases under either spelling. New leases use the legacy
// name, and every client keeps sharing .wtc-status-watchers.lock.
var statusWatcherDirs = []string{statusWatcherDir, ".taba-status-watchers"}

// RegisterStatusWatcher binds a live view to its resolved collection, even when
// launched from elsewhere or through a relative executable. Retirement marks
// its lease to request cancellation and waits for cleanup to release the lock.
// A removed or replaced lease also cancels the view. No persisted PID is
// trusted, so stale leases cannot signal a reused process.
func (c *Context) RegisterStatusWatcher(stop func()) (func(), error) {
	return c.RegisterStatusWatcherContext(context.Background(), stop)
}

func (c *Context) RegisterStatusWatcherContext(ctx context.Context, stop func()) (func(), error) {
	if RetiredFolder(c.Collection) {
		return nil, retiredFolderError(c.Collection)
	}
	var cleanup func()
	err := c.withStatusWatcherLockContext(ctx, func() error {
		// Retirement holds this same lock through folder removal. Reopen after
		// acquiring it so an already-discovered context cannot revive a retire.
		if _, err := OpenCollectionContext(ctx, c.Collection); err != nil {
			return err
		}
		var err error
		cleanup, err = c.registerStatusWatcher(stop)
		return err
	})
	return cleanup, err
}

func (c *Context) registerStatusWatcher(stop func()) (func(), error) {
	return c.registerStatusWatcherWithEvents(stop, statusResourceLeaseEvents)
}

func (c *Context) registerStatusWatcherWithEvents(stop func(), watch func(*os.File) (<-chan struct{}, func())) (func(), error) {
	dir := filepath.Join(c.Collection, statusWatcherDir)
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("status watcher registry must be a directory")
	}
	f, err := os.CreateTemp(dir, "watcher-*")
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = os.Remove(f.Name())
		_ = f.Close()
		return nil, err
	}
	identity, err := f.Stat()
	if err != nil {
		_ = os.Remove(f.Name())
		_ = f.Close()
		return nil, err
	}
	events, closeEvents := watch(f)
	done := make(chan struct{})
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			close(done)
			closeEvents()
			if current, err := os.Lstat(f.Name()); err == nil && os.SameFile(identity, current) {
				_ = os.Remove(f.Name())
			}
			_ = f.Close()
			// Release the lease before taking the watcher lock: retirement
			// may own that lock while it waits for this watcher to stop.
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			_ = c.withStatusWatcherLockContext(ctx, func() error {
				_ = os.Remove(dir) // Only remove an empty registry.
				return nil
			})
		})
	}
	go func() {
		every := 500 * time.Millisecond
		if events != nil {
			every = 5 * time.Second
		}
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			// Also check immediately after installing the notifications, so
			// a change during registration cannot be lost.
			current, err := os.Lstat(f.Name())
			if err != nil || !os.SameFile(identity, current) || current.Size() != 0 {
				stop()
				return
			}
			select {
			case <-done:
				return
			case <-ticker.C:
			case _, ok := <-events:
				if !ok {
					events = nil
					ticker.Reset(500 * time.Millisecond)
				}
			}
		}
	}()
	return cleanup, nil
}

func stopRegisteredStatusWatchers(target string) error {
	// Reject a malformed registry before stopping any lease.
	var dirs []string
	for _, name := range statusWatcherDirs {
		dir := filepath.Join(target, name)
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("status watcher registry must be a directory")
		}
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		if err := stopStatusWatcherRegistry(dir); err != nil {
			return err
		}
	}
	return nil
}

func stopStatusWatcherRegistry(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "watcher-") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		f := os.NewFile(uintptr(fd), path)
		err = stopStatusWatcherLease(f, path)
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func stopStatusWatcherLease(f *os.File, path string) error {
	identity, err := f.Stat()
	if err != nil || !identity.Mode().IsRegular() {
		return fmt.Errorf("invalid status watcher lease")
	}
	if err := f.Truncate(1); err != nil {
		return err
	}
	err = retry.Poll(context.Background(), retry.Policy{Every: 25 * time.Millisecond, Within: 5 * time.Second}, flockAttempt(int(f.Fd())))
	if errors.Is(err, retry.ErrExhausted) {
		return fmt.Errorf("status watcher did not stop before retirement")
	}
	if err != nil {
		return err
	}
	if current, err := os.Lstat(path); err == nil && os.SameFile(identity, current) {
		return os.Remove(path)
	}
	return nil // A stale lease never signals a process.
}

// This lock covers only watcher registration, cleanup and retirement. Service
// provisioning must not hold it: a live view needs to open during startup.
func (c *Context) withStatusWatcherLock(fn func() error) error {
	unlock, err := c.lockStatusWatchers()
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

func (c *Context) lockStatusWatchers() (func(), error) {
	return c.lockStatusWatchersContext(context.Background())
}

func (c *Context) lockStatusWatchersContext(ctx context.Context) (func(), error) {
	path := filepath.Join(c.Collection, ".wtc-status-watchers.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	locked := false
	defer func() {
		if !locked {
			_ = f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("status watcher lock must be a regular file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	err = retry.Poll(ctx, retry.Policy{Every: 25 * time.Millisecond, Within: 5 * time.Second}, flockAttempt(fd))
	if errors.Is(err, retry.ErrExhausted) {
		return nil, fmt.Errorf("status watcher lifecycle is busy")
	}
	if err != nil {
		return nil, err
	}
	locked = true
	return func() { _ = f.Close() }, nil // Close releases the flock.
}

func (c *Context) withStatusWatcherLockContext(ctx context.Context, fn func() error) error {
	unlock, err := c.lockStatusWatchersContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
