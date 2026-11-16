package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// The existing budget lock guards background Bitbucket confirmations and
// finite explicit requests on either forge. Ordinary reads yield to foreground
// leases; background confirmations otherwise retain their bounded FIFO. Older
// clients share accounting but do not understand foreground priority.
const statusPendingLifetime = 5 * time.Minute
const statusPendingLimit = 256

type statusPendingConfirmation struct {
	Key        string    `json:"key"`
	Seen       time.Time `json:"seen"`
	Foreground bool      `json:"foreground,omitempty"`
	Expires    time.Time `json:"expires,omitempty"`
}

func statusConfirmationKey(identity string) string {
	if identity == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

// Caller holds the forge budget lock. Never follow a queue symlink or read
// unbounded/corrupt state into the admission decision.
func statusReadPendingConfirmations(forge string, now time.Time) []statusPendingConfirmation {
	if forge != "bitbucket.org" && forge != "github.com" {
		return nil
	}
	path := statusForgeStatePath("pending", forge)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	var saved []statusPendingConfirmation
	if err != nil || json.Unmarshal(raw, &saved) != nil || len(saved) > statusPendingLimit {
		return nil
	}
	queue := make([]statusPendingConfirmation, 0, len(saved))
	seen := map[string]bool{}
	for _, item := range saved {
		if item.Foreground && (item.Expires.IsZero() || !now.Before(item.Expires)) {
			continue
		}
		if len(item.Key) != 64 || item.Seen.After(now) || now.Sub(item.Seen) >= statusPendingLifetime || seen[item.Key] {
			continue
		}
		if _, err := hex.DecodeString(item.Key); err != nil {
			continue
		}
		seen[item.Key] = true
		queue = append(queue, item)
	}
	return queue
}

func statusQueueConfirmation(queue []statusPendingConfirmation, key string, now time.Time) []statusPendingConfirmation {
	for i := range queue {
		if queue[i].Key == key {
			queue[i].Seen = now
			return queue
		}
	}
	if len(queue) < statusPendingLimit {
		queue = append(queue, statusPendingConfirmation{Key: key, Seen: now})
	}
	return queue
}

func statusSavePendingConfirmations(forge string, queue []statusPendingConfirmation) bool {
	if forge != "bitbucket.org" && forge != "github.com" {
		return false
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		return false
	}
	return statusWriteForgeEntryObserved("pending", forge, "", "", raw, time.Time{})
}

// A foreground lease is owned by one finite request, not by the next poll of a
// view. Seen also expires at the deadline in released readers that ignore the
// additive fields. Queue entries grant admission only; they never execute work.
func statusQueueForeground(queue []statusPendingConfirmation, key string, deadline time.Time) ([]statusPendingConfirmation, bool) {
	for _, item := range queue {
		if item.Key == key {
			return queue, true
		}
	}
	if len(queue) >= statusPendingLimit {
		// A manual request may displace a background reservation, never another
		// foreground request. Its owner can retry through ordinary admission.
		last := len(queue) - 1
		if queue[last].Foreground {
			return queue, false
		}
		queue = queue[:last]
	}
	at := 0
	for at < len(queue) && queue[at].Foreground {
		at++
	}
	queue = append(queue, statusPendingConfirmation{})
	copy(queue[at+1:], queue[at:])
	queue[at] = statusPendingConfirmation{Key: key, Foreground: true, Expires: deadline, Seen: deadline.Add(-statusPendingLifetime)}
	return queue, true
}

func statusRemovePending(queue []statusPendingConfirmation, key string) []statusPendingConfirmation {
	for i, item := range queue {
		if item.Key == key {
			return append(queue[:i], queue[i+1:]...)
		}
	}
	return queue
}

// A list or settled cache can resolve a queued confirmation before its turn.
func statusForgetConfirmation(forge, identity string) {
	if forge != "bitbucket.org" {
		return
	}
	unlock, err := statusLockCache(statusForgeStatePath("budget", forge))
	if err != nil {
		return
	}
	defer unlock()
	queue := statusReadPendingConfirmations(forge, time.Now())
	key := statusConfirmationKey(identity)
	for i, item := range queue {
		if item.Key == key {
			statusSavePendingConfirmations(forge, append(queue[:i], queue[i+1:]...))
			return
		}
	}
}
