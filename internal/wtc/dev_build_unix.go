//go:build unix

package wtc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"taba.sh/taba/internal/retry"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func lockDevFile(ctx context.Context, path string) (func(), error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Mode()&0022 != 0 {
		f.Close()
		return nil, fmt.Errorf("unsafe dev-mode lock")
	}
	if err := retry.Poll(ctx, retry.Policy{Every: 25 * time.Millisecond}, flockAttempt(fd)); err != nil {
		f.Close()
		return nil, err
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || !os.SameFile(info, current) {
		f.Close()
		return nil, fmt.Errorf("dev lock was removed or replaced while waiting")
	}
	return func() { _ = f.Close() }, nil
}

func lockDevRetirement(collection string) (func(), error) {
	used, err := preflightDevRetirement(collection)
	if err != nil {
		return nil, err
	}
	root, err := os.Lstat(collection)
	if err != nil {
		return nil, err
	}
	if !root.IsDir() {
		return nil, fmt.Errorf("unsafe dev collection root")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-build.lock"))
	if err != nil {
		return nil, err
	}
	policy, err := lockDevFile(ctx, filepath.Join(collection, ".wtc-dev-mode.lock"))
	if err != nil {
		build()
		return nil, err
	}
	current, err := os.Lstat(collection)
	if err != nil || !os.SameFile(root, current) || used && (!ownedByCurrentUser(current) || current.Mode()&0022 != 0) {
		policy()
		build()
		return nil, fmt.Errorf("dev collection root changed during retirement")
	}
	return func() { policy(); build() }, nil
}
