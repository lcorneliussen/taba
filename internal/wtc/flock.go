package wtc

import (
	"context"

	"golang.org/x/sys/unix"
	"taba.sh/taba/internal/retry"
)

// flockAttempt takes an exclusive lock on fd, or finds another holder and
// changes nothing.
func flockAttempt(fd int) retry.Idempotent {
	return func(context.Context) (bool, error) {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return false, nil
		}
		return err == nil, err
	}
}
