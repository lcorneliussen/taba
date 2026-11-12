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

// Only unresolved by-number confirmations reserve admission. The existing
// budget lock guards this bounded FIFO; ordinary traffic resumes after an
// abandoned reservation expires. Older clients do not honor these reservations.
const statusPendingLifetime = 5 * time.Minute
const statusPendingLimit = 256

type statusPendingConfirmation struct {
	Key  string    `json:"key"`
	Seen time.Time `json:"seen"`
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

func statusSavePendingConfirmations(forge string, queue []statusPendingConfirmation) {
	raw, err := json.Marshal(queue)
	if err == nil {
		statusWriteForgeEntry("pending", forge, "", "", raw)
	}
}
