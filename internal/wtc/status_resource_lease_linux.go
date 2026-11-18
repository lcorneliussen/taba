package wtc

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

func statusResourceLeaseEvents(lease *os.File) (<-chan struct{}, func()) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, func() {}
	}
	f := os.NewFile(uintptr(fd), "status-lease-events")
	mask := uint32(unix.IN_MODIFY | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_MOVED_TO)
	for _, path := range []string{lease.Name(), filepath.Dir(lease.Name()), filepath.Dir(filepath.Dir(lease.Name()))} {
		if _, err := unix.InotifyAddWatch(fd, path, mask|unix.IN_DONT_FOLLOW); err != nil {
			_ = f.Close()
			return nil, func() {}
		}
	}
	events, exited := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(exited)
		defer close(events)
		defer f.Close()
		var buf [4096]byte
		for {
			if _, err := f.Read(buf[:]); err != nil {
				return // Closure/error restores polling unless cleanup ended it.
			}
			select {
			case events <- struct{}{}:
			default:
			}
		}
	}()
	var once sync.Once
	return events, func() {
		once.Do(func() {
			_ = f.Close() // Interrupts the pollable read; no helper child.
			<-exited
		})
	}
}
