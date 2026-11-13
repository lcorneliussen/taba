package wtc

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Local instance evidence belongs to the same ownership journal/host observation,
// never a provider wire result. Process argv/cwd are hashed, not persisted, and
// the existing per-PID reader deliberately does not inspect its environment.
type tunnelProcessIdentity struct {
	PID       int    `json:"pid"`
	Start     string `json:"start"`
	Boot      string `json:"boot"`
	Execution string `json:"execution"`
}

func (p tunnelProcessIdentity) validate() error {
	if p.PID <= 1 || p.Start == "" || len(p.Start) > 128 || p.Boot == "" || len(p.Boot) > 128 || strings.ContainsAny(p.Start+p.Boot, "\x00\r\n") || !validTunnelDigest(p.Execution) {
		return fmt.Errorf("invalid owned process instance")
	}
	return nil
}
func tunnelBootIdentity() (string, error) {
	if runtime.GOOS != "linux" {
		return runtime.GOOS, nil
	}
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || len(data) > 128 {
		return "", fmt.Errorf("local boot identity unavailable")
	}
	return strings.TrimSpace(string(data)), nil
}
func readTunnelProcess(pid int) (tunnelProcessIdentity, string) {
	if pid <= 1 {
		return tunnelProcessIdentity{}, "unknown"
	}
	boot, err := tunnelBootIdentity()
	if err != nil {
		return tunnelProcessIdentity{}, "unknown"
	}
	info, ok := readStatusWatcherProcess(pid)
	if !ok {
		if unix.Kill(pid, 0) == unix.ESRCH {
			return tunnelProcessIdentity{}, "absent"
		}
		if runtime.GOOS == "linux" {
			data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
			if err == nil {
				end := strings.LastIndexByte(string(data), ')')
				if end >= 0 {
					fields := strings.Fields(string(data[end+1:]))
					if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
						return tunnelProcessIdentity{}, "absent"
					}
				}
			}
		}
		return tunnelProcessIdentity{}, "unknown"
	}
	identity := tunnelProcessIdentity{PID: pid, Start: info.start, Boot: boot, Execution: tunnelDigest([]byte(info.args + "\x00" + info.cwd))}
	if identity.validate() != nil {
		return tunnelProcessIdentity{}, "unknown"
	}
	return identity, "alive"
}
func currentTunnelProcess() (tunnelProcessIdentity, error) {
	p, state := readTunnelProcess(os.Getpid())
	if state != "alive" {
		return p, fmt.Errorf("owned local process identity unavailable")
	}
	return p, nil
}
func tunnelProcessState(expected tunnelProcessIdentity, read func(int) (tunnelProcessIdentity, string)) string {
	if expected.validate() != nil {
		return "unknown"
	}
	current, state := read(expected.PID)
	if state != "alive" {
		return state
	}
	if current.PID != expected.PID || current.Start != expected.Start || current.Boot != expected.Boot {
		return "absent"
	} // original instance ended; never adopt its replacement
	if current.Execution != expected.Execution {
		return "unknown"
	}
	return "alive"
}

// A stop targets only a recorded child of this owned foreground instance.
// Revalidation follows the existing watcher pattern, with an OS process handle
// opened before the final reads. A changed/reused PID is never signalled. This
// is not an OS sandbox or a claim about arbitrary unrecorded descendants.
func stopTunnelOwnedChild(ctx context.Context, expected tunnelProcessIdentity) error {
	process, err := os.FindProcess(expected.PID)
	if err != nil {
		return fmt.Errorf("owned child handle unavailable")
	}
	defer process.Release()
	return stopTunnelChildWith(ctx, expected, readTunnelProcess, func() error { return process.Signal(syscall.SIGTERM) })
}
func stopTunnelChildWith(ctx context.Context, expected tunnelProcessIdentity, read func(int) (tunnelProcessIdentity, string), signal func() error) error {
	for range 2 {
		switch tunnelProcessState(expected, read) {
		case "absent":
			return nil
		case "alive":
		default:
			return fmt.Errorf("owned child identity is uncertain; retain session")
		}
	}
	if err := signal(); err != nil && tunnelProcessState(expected, read) != "absent" {
		return fmt.Errorf("owned child stop unconfirmed")
	}
	bound, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		switch tunnelProcessState(expected, read) {
		case "absent":
			return nil
		case "unknown":
			// Exit can briefly make argv/cwd unreadable before positive absence.
			// Never signal again; wait within the same bound for exact disposal proof.
		}
		select {
		case <-bound.Done():
			return fmt.Errorf("owned child stop pending")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
