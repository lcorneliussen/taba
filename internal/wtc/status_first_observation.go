package wtc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// First observations (#548). A PR that no status view has observed yet shows
// nothing useful until one read succeeds, yet behind the Bitbucket
// confirmation FIFO it competes with every pane's routine rechecks, and
// released clients take every refill they wait for in the budget lock. So
// first reads get their own bounded share of the one shared bucket:
//
//   - At most one first read per statusFirstObservationGap (two refills), for
//     all processes together, recorded beside the budget file. Repeated
//     startups or many unobserved PRs therefore never claim more than half
//     the refill rate; confirmations and ordinary reads keep the rest.
//   - When a turn is open and the bucket is empty, the reader waits inside the
//     budget lock for the next refill, at most statusBitbucketEvery, as a
//     released FIFO head does; a released client cannot take that token.
//   - Waiters are kept oldest first. For one refill after a turn opens only
//     the oldest waiter that asked recently may take it; after that, any first
//     read may, so a waiter that went away delays the others by at most one
//     refill per turn. A waiter not seen for statusOrdinaryLifetime is dropped.
//   - Foreground leases and provider cooldowns still come first, and a refused
//     first read keeps its ordinary or confirmation place as before.
//
// What counts as a first read is decided by the caller from cached evidence
// (statusFirstObservationDue). Admission never reads the provider.

type statusFirstObservationKey struct{}

type statusFirstDueKey struct{}

// statusFirstObservationNeeded notes that the PR being enriched has never
// been observed. Only its own detail read takes a first turn
// (statusFirstObservationRead); shared reads such as its repository's open
// list are ordinary work for every PR there.
func statusFirstObservationNeeded(ctx context.Context) context.Context {
	return context.WithValue(ctx, statusFirstDueKey{}, true)
}

// statusFirstObservationRead is ctx for the admission of a PR's detail read:
// a first read when the enrichment found the PR unobserved.
func statusFirstObservationRead(ctx context.Context, forge, slug, number string) context.Context {
	if due, _ := ctx.Value(statusFirstDueKey{}).(bool); due {
		return statusFirstObservation(ctx, forge, slug, number)
	}
	return ctx
}

// statusFirstObservation marks reads made for a PR's first observation. The
// key is a digest of the PR identity; it carries no credential data.
func statusFirstObservation(ctx context.Context, forge, slug, number string) context.Context {
	return context.WithValue(ctx, statusFirstObservationKey{}, statusConfirmationKey("first\x00"+forge+"\x00"+slug+"\x00"+number))
}

func statusFirstObservationIdentity(ctx context.Context) string {
	identity, _ := ctx.Value(statusFirstObservationKey{}).(string)
	return identity
}

// statusFirstObservationGap is the shortest time between two first reads.
func statusFirstObservationGap(forge string) time.Duration {
	_, every := statusForgeAllowance(forge)
	return 2 * every
}

// statusObservedMarkKind records that some status view observed a PR, so it
// no longer needs a first read. It is scheduling state, not an observation:
// it holds no PR facts and is shared like the budget, so a credential context
// whose own observations stay private still does not ask for a first read on
// every pass.
const statusObservedMarkKind = "observed-v1"

func statusMarkObserved(forge, slug, number string) {
	if forge != "bitbucket.org" || slug == "" || number == "" {
		return
	}
	if _, ok := statusReadForgeEntryAny(statusObservedMarkKind, forge, slug, number); ok {
		return
	}
	statusWriteForgeEntry(statusObservedMarkKind, forge, slug, number, []byte("1"))
}

// statusMarkObservedDetail marks a PR observed once a read produced its state.
func statusMarkObservedDetail(forge, slug string, detail statusPRDetail) {
	if detail.ObservedAt != "" && detail.State != "" && detail.State != "UNKNOWN" {
		statusMarkObserved(forge, slug, detail.Number)
	}
}

// statusFirstObservationDue reports whether a PR still needs its first read:
// nothing marks it observed and nothing in this credential context holds an
// observation of it. Cache reads only.
func statusFirstObservationDue(ctx context.Context, record PRRecord, slug, forge string) bool {
	if forge != "bitbucket.org" || slug == "" || record.MergedOn != "" {
		return false
	}
	if _, ok := statusReadForgeEntryAny(statusObservedMarkKind, forge, slug, record.Number); ok {
		return false
	}
	if _, ok := statusReadForgeEntryAnyContext(ctx, "pr", forge, slug, record.Number); ok {
		return false
	}
	_, retained := statusResourceRetainedMerge(ctx, record, slug, forge)
	return !retained
}

type statusFirstWaiters struct {
	Last  time.Time              `json:"last,omitzero"`
	Queue []statusOrdinaryWaiter `json:"queue"`
}

// A sibling of the budget file; caller holds the budget lock. Released
// clients never read or write it.
func statusFirstWaitPath(forge string) string {
	return statusForgeStatePath("budget", forge) + ".first"
}

// Never follow a symlink or trust unbounded, corrupt or future state.
func statusReadFirstWaiters(forge string, now time.Time) statusFirstWaiters {
	var saved statusFirstWaiters
	path := statusFirstWaitPath(forge)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return saved
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return saved
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || json.Unmarshal(raw, &saved) != nil || len(saved.Queue) > statusPendingLimit {
		return statusFirstWaiters{}
	}
	if saved.Last.After(now) {
		saved.Last = time.Time{}
	}
	waiters := statusFirstWaiters{Last: saved.Last}
	seen := map[string]bool{}
	for _, item := range saved.Queue {
		if len(item.Key) != 64 || seen[item.Key] || item.Since.After(now) || item.Seen.After(now) || now.Sub(item.Seen) >= statusOrdinaryLifetime {
			continue
		}
		if _, err := hex.DecodeString(item.Key); err != nil {
			continue
		}
		seen[item.Key] = true
		waiters.Queue = append(waiters.Queue, item)
	}
	return waiters
}

// A lost turn record would allow a second first read in the same turn, so an
// admission whose record cannot be written is refused.
func statusSaveFirstWaiters(forge string, waiters statusFirstWaiters) bool {
	if len(waiters.Queue) == 0 && waiters.Last.IsZero() {
		err := os.Remove(statusFirstWaitPath(forge))
		return err == nil || os.IsNotExist(err)
	}
	raw, err := json.Marshal(waiters)
	return err == nil && os.WriteFile(statusFirstWaitPath(forge), raw, 0600) == nil
}

// register keeps key's place (or adds it at the tail) and notes that it asked.
func (w *statusFirstWaiters) register(key string, now time.Time) int {
	for i := range w.Queue {
		if w.Queue[i].Key == key {
			w.Queue[i].Seen = now
			return i
		}
	}
	if len(w.Queue) >= statusPendingLimit {
		return -1
	}
	w.Queue = append(w.Queue, statusOrdinaryWaiter{Key: key, Since: now, Seen: now})
	return len(w.Queue) - 1
}

// rank is key's place among the waiters that asked within grace; the oldest
// of those is the head.
func (w *statusFirstWaiters) rank(key string, now time.Time, grace time.Duration) int {
	rank := 0
	for _, item := range w.Queue {
		if item.Key == key {
			return rank
		}
		if now.Sub(item.Seen) < grace {
			rank++
		}
	}
	return -1
}

// statusTakeFirstToken is the first-observation turn for one Bitbucket read;
// caller holds the budget lock and has refused foreground heads. It reports
// whether the read was admitted and, if not, when its turn could earliest
// come. queue is the confirmation FIFO; this function saves it before waiting
// in the lock and on admission (without the answered confirmation).
func statusTakeFirstToken(ctx context.Context, forge, key, confirmation, path string, queue *[]statusPendingConfirmation, now time.Time) (bool, time.Time) {
	burst, every := statusForgeAllowance(forge)
	gap := statusFirstObservationGap(forge)
	waiters := statusReadFirstWaiters(forge, now)
	at := waiters.register(key, now)
	if at < 0 {
		return false, time.Time{}
	}
	open := waiters.Last.Add(gap)
	// Recently asked: within one pass of a background view.
	grace := statusPendingLifetime
	rank := waiters.rank(key, now, grace)
	eligible := !now.Before(open) && (rank == 0 || !now.Before(open.Add(every)))
	if !eligible || burst <= 1 || statusForgeCooling(forge) {
		statusSaveFirstWaiters(forge, waiters)
		return false, open.Add(time.Duration(max(rank, 0)) * gap)
	}
	tokens, updated := statusReadForgeBudget(path, burst, every, now)
	if tokens == 0 {
		delay := updated.Add(every).Sub(now)
		if delay <= 0 || delay > statusBitbucketEvery {
			statusSaveFirstWaiters(forge, waiters)
			return false, updated.Add(every)
		}
		statusSaveFirstWaiters(forge, waiters)
		statusSavePendingConfirmations(forge, *queue)
		// Provider I/O stays outside this wait, as for the FIFO head.
		if statusAdmissionWait(ctx, delay) != nil {
			return false, time.Time{}
		}
		now = statusAdmissionNow()
		if statusForgeCooling(forge) {
			return false, time.Time{}
		}
		tokens, updated = statusReadForgeBudget(path, burst, every, now)
		waiters = statusReadFirstWaiters(forge, now)
		at = waiters.register(key, now)
		if tokens < 1 || at < 0 {
			statusSaveFirstWaiters(forge, waiters)
			return false, updated.Add(every)
		}
	}
	if ctx.Err() != nil {
		return false, time.Time{}
	}
	waiters.Queue = append(waiters.Queue[:at], waiters.Queue[at+1:]...)
	waiters.Last = now
	if !statusSaveFirstWaiters(forge, waiters) {
		return false, time.Time{}
	}
	// The read also answers the confirmation it queued for the same PR.
	if confirmation != "" {
		*queue = statusRemovePending(*queue, confirmation)
	}
	statusSavePendingConfirmations(forge, *queue)
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens-1, updated.UnixNano())), 0600) == nil, time.Time{}
}
