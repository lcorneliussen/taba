package wtc

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// Watch the lease and its containing namespace before handing registration to
// the caller. No persisted process identity participates in cancellation.
func statusResourceLeaseEvents(lease *os.File) (<-chan struct{}, func()) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, func() {}
	}
	unix.CloseOnExec(kq)
	var dirs []int
	closeFiles := func() {
		for _, fd := range dirs {
			_ = unix.Close(fd)
		}
		_ = unix.Close(kq)
	}
	changes := []unix.Kevent_t{{Ident: 1, Filter: unix.EVFILT_USER, Flags: unix.EV_ADD | unix.EV_CLEAR}}
	fds := []int{int(lease.Fd())}
	for _, path := range []string{filepath.Dir(lease.Name()), filepath.Dir(filepath.Dir(lease.Name()))} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			closeFiles()
			return nil, func() {}
		}
		dirs = append(dirs, fd)
		fds = append(fds, fd)
	}
	for _, fd := range fds {
		changes = append(changes, unix.Kevent_t{Ident: uint64(fd), Filter: unix.EVFILT_VNODE, Flags: unix.EV_ADD | unix.EV_CLEAR,
			Fflags: unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB | unix.NOTE_DELETE | unix.NOTE_RENAME | unix.NOTE_REVOKE})
	}
	if _, err := unix.Kevent(kq, changes, nil, nil); err != nil {
		closeFiles()
		return nil, func() {}
	}
	events, done, exited := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		defer close(events)
		var received [4]unix.Kevent_t
		for {
			n, err := unix.Kevent(kq, nil, received[:], nil)
			if err == unix.EINTR {
				continue
			}
			select {
			case <-done:
				return
			default:
			}
			if err != nil {
				return // A closed stream restores the polling fallback.
			}
			for _, event := range received[:n] {
				if event.Flags&unix.EV_ERROR != 0 {
					return
				}
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
			close(done)
			_, _ = unix.Kevent(kq, []unix.Kevent_t{{Ident: 1, Filter: unix.EVFILT_USER, Fflags: unix.NOTE_TRIGGER}}, nil, nil)
			<-exited
			closeFiles()
		})
	}
}
