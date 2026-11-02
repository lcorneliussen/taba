package wtc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
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
	if RetiredFolder(c.Collection) {
		return nil, retiredFolderError(c.Collection)
	}
	var cleanup func()
	err := c.withStatusWatcherLock(func() error {
		// Retirement holds this same lock through folder removal. Reopen after
		// acquiring it so an already-discovered context cannot revive a retire.
		if _, err := OpenCollection(c.Collection); err != nil {
			return err
		}
		var err error
		cleanup, err = c.registerStatusWatcher(stop)
		return err
	})
	return cleanup, err
}

func (c *Context) registerStatusWatcher(stop func()) (func(), error) {
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
	done := make(chan struct{})
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			close(done)
			if current, err := os.Lstat(f.Name()); err == nil && os.SameFile(identity, current) {
				_ = os.Remove(f.Name())
			}
			_ = f.Close()
			// Release the lease before taking the watcher lock: retirement
			// may own that lock while it waits for this watcher to stop.
			_ = c.withStatusWatcherLock(func() error {
				_ = os.Remove(dir) // Only remove an empty registry.
				return nil
			})
		})
	}
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				current, err := os.Lstat(f.Name())
				if err != nil || !os.SameFile(identity, current) || current.Size() != 0 {
					stop()
					return
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if current, err := os.Lstat(path); err == nil && os.SameFile(identity, current) {
				return os.Remove(path)
			}
			return nil // A stale lease never signals a process.
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("status watcher did not stop before retirement")
		}
		time.Sleep(25 * time.Millisecond)
	}
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
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			locked = true
			return func() { _ = f.Close() }, nil // Close releases the flock.
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("status watcher lifecycle is busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
}
