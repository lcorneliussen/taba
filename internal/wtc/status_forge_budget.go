package wtc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"taba.sh/taba/internal/envcompat"
)

// Status talks to Bitbucket and GitHub from every open status pane. Those
// forges bill the calls to one user. A shared cache, one in-flight fetch per
// record, and a shared allowance keep the panes from spending the hour on
// the first few minutes. A rate-limit response pauses that forge and status
// keeps showing the last saved record.

const (
	statusForgeCoolDefault = 15 * time.Minute
	statusBitbucketBurst   = 8
	statusBitbucketEvery   = 8 * time.Second
	statusGitHubBurst      = 16
	statusGitHubEvery      = 2 * time.Second
)

var (
	errForgeCooling = errors.New("forge rate limit cooling down")
	errForgeBudget  = errors.New("forge request budget spent")
)

var statusRateLimitedText = regexp.MustCompile(`(?i)http\s+429|returned\s+429|too many requests|rate limit exceeded|rate limit for this resource|secondary rate limit`)
var statusRetryAfterText = regexp.MustCompile(`(?i)retry[- ]after:?\s*(\d+)`)

func statusRateLimited(err error) bool {
	return err != nil && statusRateLimitedText.MatchString(err.Error())
}

func statusRetryAfter(err error) time.Duration {
	if err == nil {
		return statusForgeCoolDefault
	}
	match := statusRetryAfterText.FindStringSubmatch(err.Error())
	if match == nil {
		return statusForgeCoolDefault
	}
	seconds, err := strconv.Atoi(match[1])
	if err != nil || seconds < 1 {
		return statusForgeCoolDefault
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

// statusForgeAllowance is the shared burst and the gap between later calls.
// WTC_FORGE_MIN_INTERVAL=0 turns the allowance off. A positive number of
// seconds allows one call, then waits that long.
func statusForgeAllowance(forge string) (burst int, every time.Duration) {
	if raw := envcompat.Getenv("WTC_FORGE_MIN_INTERVAL"); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds <= 0 {
			return 1 << 30, time.Second
		}
		return 1, time.Duration(seconds) * time.Second
	}
	if forge == "bitbucket.org" {
		return statusBitbucketBurst, statusBitbucketEvery
	}
	return statusGitHubBurst, statusGitHubEvery
}

func statusForgeStatePath(prefix, forge string) string {
	return statusForgeEntryPath(prefix, forge, "", "")
}

func statusForgeCooling(forge string) bool {
	return time.Now().Before(statusForgeCoolingUntil(forge))
}

func statusForgeCoolingUntil(forge string) time.Time {
	raw, err := os.ReadFile(statusForgeStatePath("cool", forge))
	if err != nil {
		return time.Time{}
	}
	until, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, until)
}

func statusForgeCool(forge string, retry time.Duration) {
	_, _ = statusForgeCoolContext(context.Background(), forge, retry)
}

// Return the actual provider cooldown even if cancellation prevents recording
// it. Computing the deadline before waiting avoids dating a delayed write as a
// new provider observation. Existing callers retain their compatibility entry.
func statusForgeCoolContext(ctx context.Context, forge string, retry time.Duration) (time.Time, error) {
	if retry <= 0 {
		retry = statusForgeCoolDefault
	}
	until := time.Now().Add(retry)
	path := statusForgeStatePath("cool", forge)
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return until, err
	}
	defer unlock()
	if existing := statusForgeCoolingUntil(forge); existing.After(until) {
		return existing, nil
	}
	if err := ctx.Err(); err != nil {
		return until, err
	}
	if !statusWriteForgeEntryObservedContext(ctx, "cool", forge, "", "", []byte(strconv.FormatInt(until.UnixNano(), 10)), time.Time{}) {
		return until, errors.New("provider cooldown publication failed")
	}
	// A completed atomic write remains valid after late cancellation.
	return until, nil
}

func statusTakeForgeToken(forge string) bool {
	return statusTakeForgeTokenFor(forge, "")
}

func statusTakeForgeTokenFor(forge, confirmation string) bool {
	return statusTakeForgeTokenForContext(context.Background(), forge, confirmation)
}

func statusTakeForgeTokenForContext(ctx context.Context, forge, confirmation string) bool {
	admitted, _ := statusTakeForgeTokenRetry(ctx, forge, confirmation)
	return admitted
}

// Clock seams for the admission decision only; tests drive refills and waits
// on a fake clock. Production uses the wall clock and the bounded refill wait.
var (
	statusAdmissionNow  = time.Now
	statusAdmissionWait = statusWaitRefresh
)

// Bitbucket admission contract, all under the one budget lock and
// debiting the one shared bucket, so the burst, refill rate and cooldown are
// unchanged:
//
//   - Confirmations stay FIFO and reserve one token each, but at most
//     burst-1: with a burst above one, a full bucket always has a token an
//     ordinary read may take.
//   - A refused ordinary read joins a FIFO of ordinary waiters, keyed by the
//     identity of what it reads. Once the head has waited one full refill
//     (burst*every) since it joined or since the last such turn,
//     confirmations leave the last token for it, without waiting in the lock
//     for another, and the head may take any token. That turn is recorded:
//     this path yields one ordinary read per window, in waiter order.
//   - A waiter not seen for statusOrdinaryLifetime is dropped; a head that
//     has not asked again for statusPendingLifetime since its turn opened or
//     since it last asked, whichever is later, moves to the tail. A dormant
//     waiter thus delays the others by at most that long.
//
// Bound (finite load: waiters retry within statusPendingLifetime, fewer than
// statusPendingLimit wait): the waiter at position p is admitted within
// (p+1) * (window + one refill + its retry interval), plus the grace of any
// dormant waiter ahead, however many confirmations arrive, plus one refill for
// each first-observation turn (#548, statusTakeFirstToken: at most one per two
// refills) that takes the held token. Confirmations lose
// at most one token per window to this path, otherwise only tokens left above
// their reserve. Reads that name no identity share one place in the FIFO and
// among themselves stay first come. With a one-token allowance
// (WTC_FORGE_MIN_INTERVAL) nothing can be held: the head gets a turn only
// when it finds the token unspent. Released clients share the bucket and the
// confirmation FIFO but not the waiters; they stay at least as conservative
// for ordinary reads and may take the held token.
const statusOrdinaryLifetime = 2 * statusPendingLifetime

func statusOrdinaryAge(forge string) time.Duration {
	burst, every := statusForgeAllowance(forge)
	return time.Duration(max(burst, 1)) * every
}

type statusOrdinaryIdentityKey struct{}

// statusOrdinaryRead names what an ordinary read fetches, so it keeps its own
// place among ordinary waiters. The digest carries no credential data.
func statusOrdinaryRead(ctx context.Context, kind, forge, slug, key string) context.Context {
	return context.WithValue(ctx, statusOrdinaryIdentityKey{}, statusConfirmationKey("ordinary\x00"+kind+"\x00"+forge+"\x00"+slug+"\x00"+key))
}

func statusOrdinaryIdentity(ctx context.Context) string {
	if identity, _ := ctx.Value(statusOrdinaryIdentityKey{}).(string); identity != "" {
		return identity
	}
	return statusConfirmationKey("ordinary")
}

type statusOrdinaryWaiter struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"`
	Seen  time.Time `json:"seen"`
}

type statusOrdinaryWaiters struct {
	Last  time.Time              `json:"last,omitzero"`
	Queue []statusOrdinaryWaiter `json:"queue"`
}

// A sibling of the shared budget file, like its lock: same scope, no new
// cache-identity kind. Caller holds the budget lock.
func statusOrdinaryWaitPath(forge string) string {
	return statusForgeStatePath("budget", forge) + ".ordinary"
}

// Never follow a symlink or trust unbounded, corrupt or future state.
func statusReadOrdinaryWaiters(forge string, now time.Time) statusOrdinaryWaiters {
	var saved statusOrdinaryWaiters
	path := statusOrdinaryWaitPath(forge)
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
		return statusOrdinaryWaiters{}
	}
	if saved.Last.After(now) {
		saved.Last = time.Time{}
	}
	waiters := statusOrdinaryWaiters{Last: saved.Last}
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

// statusSaveOrdinaryWaiters reports whether the state was written. A lost
// registration or removal only delays waiters; a lost turn record would let
// a served head take a second turn, so that admission is refused instead.
func statusSaveOrdinaryWaiters(forge string, waiters statusOrdinaryWaiters) bool {
	if len(waiters.Queue) == 0 && waiters.Last.IsZero() {
		err := os.Remove(statusOrdinaryWaitPath(forge))
		return err == nil || os.IsNotExist(err)
	}
	raw, err := json.Marshal(waiters)
	return err == nil && os.WriteFile(statusOrdinaryWaitPath(forge), raw, 0600) == nil
}

// turn is when the head's turn opens; zero only without waiters. A head
// quiet past its grace moves to the tail first. A moved head restarts its
// wait now, so within len(queue) rotations some head's turn lies ahead.
func (w *statusOrdinaryWaiters) turn(age time.Duration, now time.Time) (time.Time, bool) {
	moved := false
	for i := 0; i <= len(w.Queue) && len(w.Queue) > 0; i++ {
		head := w.Queue[0]
		turn := head.Since
		if w.Last.After(turn) {
			turn = w.Last
		}
		turn = turn.Add(age)
		quiet := turn
		if head.Seen.After(quiet) {
			quiet = head.Seen
		}
		if now.Before(quiet.Add(statusPendingLifetime)) {
			return turn, moved
		}
		head.Since, moved = now, true
		w.Queue = append(w.Queue[1:], head)
	}
	return time.Time{}, moved
}

func (w *statusOrdinaryWaiters) position(key string) int {
	for i, item := range w.Queue {
		if item.Key == key {
			return i
		}
	}
	return -1
}

// statusTakeForgeTokenRetry is statusTakeForgeTokenForContext that also says
// when a refused read could earliest be admitted (zero when not known).
func statusTakeForgeTokenRetry(ctx context.Context, forge, confirmation string) (bool, time.Time) {
	if ctx.Err() != nil {
		return false, time.Time{}
	}
	burst, every := statusForgeAllowance(forge)
	if burst >= 1<<30 {
		return true, time.Time{}
	}
	path := statusForgeStatePath("budget", forge)
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return false, time.Time{}
	}
	defer unlock()
	now := statusAdmissionNow()
	queue := statusReadPendingConfirmations(forge, now)
	if forge != "bitbucket.org" {
		// GitHub has no legacy confirmation FIFO. Only an owned foreground lease
		// may defer its ordinary reads; unrelated queue bytes remain untouched.
		for _, item := range queue {
			if item.Foreground {
				return false, time.Time{}
			}
		}
		queue = nil
	}
	saveQueue := func() {
		if forge == "bitbucket.org" {
			statusSavePendingConfirmations(forge, queue)
		}
	}
	admitted := len(queue) == 0 || queue[0].Key == confirmation
	if confirmation != "" && forge == "bitbucket.org" {
		queue = statusQueueConfirmation(queue, confirmation, now)
	}
	if len(queue) > 0 && queue[0].Foreground {
		saveQueue()
		return false, time.Time{}
	}
	ordinary := confirmation == ""
	// A PR's first read takes its bounded first-observation turn (#548); when
	// that turn is not open it competes as before.
	var firstRetry time.Time
	if first := statusFirstObservationIdentity(ctx); first != "" && forge == "bitbucket.org" {
		var taken bool
		taken, firstRetry = statusTakeFirstToken(ctx, forge, first, confirmation, path, &queue, now)
		if taken {
			if ordinary {
				// Its ordinary place, if any, is answered too.
				waiters := statusReadOrdinaryWaiters(forge, now)
				if at := waiters.position(statusOrdinaryIdentity(ctx)); at >= 0 {
					waiters.Queue = append(waiters.Queue[:at], waiters.Queue[at+1:]...)
					statusSaveOrdinaryWaiters(forge, waiters)
				}
			}
			return true, time.Time{}
		}
		if ctx.Err() != nil {
			return false, time.Time{}
		}
		now = statusAdmissionNow()
	}
	age := statusOrdinaryAge(forge)
	// Ordinary waiters exist only beside the Bitbucket FIFO; they are read
	// even when it is empty so a waiter keeps its place.
	var waiters statusOrdinaryWaiters
	var turn time.Time
	dirty := false
	if forge == "bitbucket.org" {
		waiters = statusReadOrdinaryWaiters(forge, now)
		turn, dirty = waiters.turn(age, now)
	}
	identity := ""
	if ordinary {
		identity = statusOrdinaryIdentity(ctx)
	}
	aged := len(queue) > 0 && len(waiters.Queue) > 0 && !turn.IsZero() && !now.Before(turn)
	// A one-token allowance cannot hold a token back; there the head only
	// competes for the token instead of waiting behind the FIFO.
	hold := 0
	if aged && burst > 1 {
		hold = 1
	}
	tokens, updated := statusReadForgeBudget(path, burst, every, now)
	// Released clients honor this existing lock but do not know the FIFO.
	// Keep its next refill for the head confirmation, bounded by the standard
	// Bitbucket interval. Provider I/O starts only after this lock is released.
	// Not while a token is held: waiting would keep the lock, and with it the
	// held token, away from the aged ordinary waiter it is held for.
	if forge == "bitbucket.org" && !ordinary && admitted && tokens == 0 && hold == 0 && !statusForgeCooling(forge) {
		delay := updated.Add(every).Sub(now)
		if delay > 0 && delay <= statusBitbucketEvery {
			saveQueue()
			// Wait at most statusBitbucketEvery while holding the budget lock;
			// provider I/O must stay outside this wait.
			if statusAdmissionWait(ctx, delay) != nil {
				return false, time.Time{}
			}
			now = statusAdmissionNow()
			tokens, updated = statusReadForgeBudget(path, burst, every, now)
			if statusForgeCooling(forge) {
				return false, time.Time{}
			}
		}
	}
	// Keep one token per queued confirmation, at most burst-1 (all of a
	// one-token allowance); spare capacity remains available to ordinary
	// reads while confirmations keep FIFO order.
	reserve := min(len(queue), max(burst-1, 1))
	need := 1
	turnTaken := false
	if ordinary {
		admitted = tokens > reserve
		if !admitted && aged && tokens >= 1 && waiters.Queue[0].Key == identity {
			admitted, turnTaken = true, true
		}
	} else {
		need += hold
	}
	if tokens < need || !admitted {
		saveQueue()
		retry := time.Time{}
		// A queued confirmation behind the head has no known turn.
		if tokens < need && (ordinary || admitted) {
			retry = updated.Add(time.Duration(need-tokens) * every)
		}
		// Register even with an empty FIFO: the next confirmation becomes head at
		// once and would otherwise win every refill it waits for in the lock.
		if ordinary && forge == "bitbucket.org" {
			at := waiters.position(identity)
			if at < 0 && len(waiters.Queue) < statusPendingLimit {
				waiters.Queue = append(waiters.Queue, statusOrdinaryWaiter{Key: identity, Since: now})
				at = len(waiters.Queue) - 1
				if at == 0 {
					turn, _ = waiters.turn(age, now)
				}
			}
			if at >= 0 {
				waiters.Queue[at].Seen, dirty = now, true
				// Earliest: every waiter ahead takes exactly one window.
				if due := turn.Add(time.Duration(at) * age); due.After(retry) {
					retry = due
				}
			}
		}
		if dirty {
			statusSaveOrdinaryWaiters(forge, waiters)
		}
		if !firstRetry.IsZero() && (retry.IsZero() || firstRetry.Before(retry)) {
			retry = firstRetry
		}
		return false, retry
	}
	if !ordinary && len(queue) > 0 && queue[0].Key == confirmation {
		queue = queue[1:]
	}
	if ctx.Err() != nil {
		return false, time.Time{}
	}
	saveQueue()
	if ordinary {
		if at := waiters.position(identity); at >= 0 {
			waiters.Queue, dirty = append(waiters.Queue[:at], waiters.Queue[at+1:]...), true
		}
		if turnTaken {
			waiters.Last, dirty = now, true
		}
	}
	if dirty && !statusSaveOrdinaryWaiters(forge, waiters) && turnTaken {
		return false, time.Time{}
	}
	if first := statusFirstObservationIdentity(ctx); first != "" && forge == "bitbucket.org" {
		// Admitted through its ordinary or confirmation place: it no longer
		// waits for a first turn, so it cannot hold a turn's exclusive refill.
		statusForgetFirstWaiter(forge, first, now)
	}
	tokens--
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens, updated.UnixNano())), 0600) == nil, time.Time{}
}

// Unlike background admission, an explicit request owns a finite cancellable
// wait. It bypasses dormant background reservations, but debits the same bucket
// under the same lock used by released clients. Holding that lock for at most
// one standard refill prevents an older consumer from stealing the reservation.
func statusTakeForegroundToken(ctx context.Context, forge string) error {
	ctx, cancel := statusRefreshContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if forge != "github.com" && forge != "bitbucket.org" {
		return errors.New("unsupported forge")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	key := statusConfirmationKey(hex.EncodeToString(nonce[:]))
	deadline, _ := ctx.Deadline()
	registered := false
	defer func() {
		if !registered {
			return
		}
		// Cleanup cannot inherit the cancelled request. If another process
		// holds the lock, the finite lease still expires in old/new readers.
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		unlock, err := statusLockCacheContext(cleanup, statusForgeStatePath("budget", forge))
		if err == nil {
			defer unlock()
			queue := statusReadPendingConfirmations(forge, time.Now())
			statusSavePendingConfirmations(forge, statusRemovePending(queue, key))
		}
	}()
	for {
		unlock, err := statusLockCacheContext(ctx, statusForgeStatePath("budget", forge))
		if err != nil {
			return err
		}
		err = func() error {
			defer unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			now := time.Now()
			if until := statusForgeCoolingUntil(forge); now.Before(until) {
				return &statusAdmissionError{cause: errForgeCooling, retry: until}
			}
			queue := statusReadPendingConfirmations(forge, now)
			var queued bool
			queue, queued = statusQueueForeground(queue, key, deadline)
			if !queued {
				return &statusAdmissionError{cause: errForgeBudget}
			}
			registered = true
			if !statusSavePendingConfirmations(forge, queue) {
				return errors.New("forge request reservation unavailable")
			}
			if queue[0].Key != key {
				return errForgeBudget
			}
			burst, every := statusForgeAllowance(forge)
			path := statusForgeStatePath("budget", forge)
			tokens, updated := statusReadForgeBudget(path, burst, every, now)
			if tokens < 1 {
				retry := updated.Add(every)
				delay := time.Until(retry)
				// Custom intervals longer than a normal provider refill defer
				// honestly instead of holding the shared lock for minutes.
				limit := statusGitHubEvery
				if forge == "bitbucket.org" {
					limit = statusBitbucketEvery
				}
				if delay > limit {
					return &statusAdmissionError{cause: errForgeBudget, retry: retry}
				}
				if err := statusWaitRefresh(ctx, max(0, delay)); err != nil {
					return err
				}
				now = time.Now()
				if until := statusForgeCoolingUntil(forge); now.Before(until) {
					return &statusAdmissionError{cause: errForgeCooling, retry: until}
				}
				tokens, updated = statusReadForgeBudget(path, burst, every, now)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if tokens < 1 {
				return &statusAdmissionError{cause: errForgeBudget, retry: updated.Add(every)}
			}
			if err := os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens-1, updated.UnixNano())), 0600); err != nil {
				return err
			}
			registered = !statusSavePendingConfirmations(forge, statusRemovePending(queue, key))
			return nil
		}()
		if err == errForgeBudget {
			if err = statusWaitRefresh(ctx, 25*time.Millisecond); err == nil {
				continue
			}
		}
		return err
	}
}

func statusWaitRefresh(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Context-aware acquisition keeps cancellation finite even behind another
// process's refill wait. A failed lock is never permission for an unaccounted
// foreground request.
func statusLockCacheContext(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if unlock, handled, err := statusAuthMemoryLock(ctx, path); handled {
		return unlock, err
	}
	if !statusSecureCacheDir(statusForgeCacheDir()) {
		return nil, errors.New("forge cache")
	}
	fd, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path+".lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("forge cache lock")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, err
		}
		if err := statusWaitRefresh(ctx, 25*time.Millisecond); err != nil {
			file.Close()
			return nil, err
		}
	}
}

// Caller holds the existing budget lock; re-reading after a wait retains the
// released two-field protocol and revalidates capacity before admission.
func statusReadForgeBudget(path string, burst int, every time.Duration, now time.Time) (int, time.Time) {
	tokens, updated := burst, now
	if raw, err := os.ReadFile(path); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) == 2 {
			if parsed, err := strconv.Atoi(fields[0]); err == nil {
				tokens = parsed
			}
			if parsed, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				updated = time.Unix(0, parsed)
			}
		}
	}
	if now.After(updated) {
		gained := int(now.Sub(updated) / every)
		if gained > 0 {
			tokens += gained
			updated = updated.Add(time.Duration(gained) * every)
		}
	}
	if tokens > burst {
		tokens = burst
		updated = now
	}
	return tokens, updated
}

func statusLockCache(path string) (func(), error) {
	if !statusSecureCacheDir(statusForgeCacheDir()) {
		return nil, errors.New("forge cache")
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
	}, nil
}

func statusWithForgeBudget(forge string, stale []byte, fetch func() ([]byte, error)) (body []byte, live bool, err error) {
	return statusWithForgeBudgetKey(forge, "", stale, fetch)
}

func statusWithForgeBudgetKey(forge, confirmation string, stale []byte, fetch func() ([]byte, error)) (body []byte, live bool, err error) {
	return statusWithForgeBudgetKeyContext(context.Background(), forge, confirmation, stale, fetch)
}

func statusWithForgeBudgetKeyContext(ctx context.Context, forge, confirmation string, stale []byte, fetch func() ([]byte, error)) (body []byte, live bool, err error) {
	body, live, _, err = statusWithForgeAdmissionContext(ctx, forge, confirmation, stale, fetch)
	return body, live, err
}

// statusRefusal records why a provider read produced no usable result (not
// admitted, cooling, rate limited or cancelled), so a caller serving older
// bytes can say so instead of reporting an unknown deferral.
type statusRefusal struct {
	reason string
	retry  time.Time
}

func (r statusRefusal) apply(detail statusPRDetail) statusPRDetail {
	if r.reason == "" {
		return detail
	}
	detail.Freshness, detail.Reason, detail.RetryAt = "deferred", r.reason, ""
	if !r.retry.IsZero() {
		detail.RetryAt = r.retry.UTC().Format(time.RFC3339Nano)
	}
	return detail
}

func statusRefusalFor(err error) statusRefusal {
	outcome := statusObservationFailure(err)
	if outcome.Freshness != "deferred" {
		return statusRefusal{}
	}
	refusal := statusRefusal{reason: outcome.Reason}
	refusal.retry, _ = time.Parse(time.RFC3339Nano, outcome.RetryAt)
	return refusal
}

func statusWithForgeAdmissionContext(ctx context.Context, forge, confirmation string, stale []byte, fetch func() ([]byte, error)) (body []byte, live bool, refused statusRefusal, err error) {
	ctx = statusAuthOperation(ctx)
	selected := statusAuthForContext(ctx, forge)
	if err := ctx.Err(); err != nil {
		return nil, false, statusRefusalFor(err), err
	}
	// GitHub identity recovery already schedules its ordinary and repair reads.
	// Reserve admission only for Bitbucket omission confirmations.
	if forge != "bitbucket.org" {
		confirmation = ""
	}
	if statusForgeCooling(forge) {
		refused = statusRefusal{reason: "provider-cooldown", retry: statusForgeCoolingUntil(forge)}
		if len(stale) > 0 {
			return stale, false, refused, nil
		}
		return nil, false, refused, errForgeCooling
	}
	if admitted, retry := statusTakeForgeTokenRetry(ctx, forge, statusConfirmationKey(confirmation)); !admitted {
		if err := ctx.Err(); err != nil {
			return nil, false, statusRefusalFor(err), err
		}
		refused = statusRefusal{reason: "shared-budget", retry: retry}
		if len(stale) > 0 {
			return stale, false, refused, nil
		}
		return nil, false, refused, errForgeBudget
	}
	if err := ctx.Err(); err != nil {
		return nil, false, statusRefusalFor(err), err
	}
	raw, err := fetch()
	if selected != statusResolveAuthContext(forge) {
		return nil, false, refused, errors.New("forge credential context changed")
	}
	if ctx.Err() != nil {
		return nil, false, statusRefusalFor(ctx.Err()), ctx.Err()
	}
	if err != nil {
		if statusRateLimited(err) {
			until, _ := statusForgeCoolContext(ctx, forge, statusRetryAfter(err))
			refused = statusRefusal{reason: "provider-cooldown", retry: until}
			if len(stale) > 0 {
				return stale, false, refused, nil
			}
		}
		return nil, false, refused, err
	}
	return raw, true, refused, nil
}

func statusSingleFlight(kind, forge, slug, key string, fetch func() ([]byte, error)) ([]byte, error) {
	return statusSingleFlightContext(context.Background(), kind, forge, slug, key, fetch)
}

func statusSingleFlightContext(ctx context.Context, kind, forge, slug, key string, fetch func() ([]byte, error)) ([]byte, error) {
	raw, _, err := statusFetchSharedContext(ctx, kind, forge, slug, key, fetch)
	return raw, err
}

func statusFetchShared(kind, forge, slug, key string, fetch func() ([]byte, error)) ([]byte, bool, error) {
	return statusFetchSharedContext(context.Background(), kind, forge, slug, key, fetch)
}

func statusFetchSharedContext(ctx context.Context, kind, forge, slug, key string, fetch func() ([]byte, error)) ([]byte, bool, error) {
	raw, live, _, err := statusFetchSharedAdmissionContext(ctx, kind, forge, slug, key, fetch)
	return raw, live, err
}

// statusFetchSharedAdmissionContext is statusFetchSharedContext that also
// reports why a read was refused when older bytes are served instead.
func statusFetchSharedAdmissionContext(ctx context.Context, kind, forge, slug, key string, fetch func() ([]byte, error)) ([]byte, bool, statusRefusal, error) {
	ctx = statusAuthOperation(ctx)
	if !statusAuthSelectionMatches(ctx, forge) {
		return nil, false, statusRefusal{}, errors.New("forge credential context changed")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, statusRefusalFor(err), err
	}
	if data, ok := statusReadForgeEntryContext(ctx, kind, forge, slug, key); ok {
		return data, false, statusRefusal{}, nil
	}
	stale, _ := statusReadForgeEntryAnyContext(ctx, kind, forge, slug, key)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, kind, forge, slug, key))
	if lockErr != nil {
		refused := statusRefusalFor(lockErr)
		if refused.reason == "" {
			refused.reason = "cache-lock"
		}
		return nil, false, refused, lockErr
	}
	defer unlock()
	if data, ok := statusReadForgeEntryContext(ctx, kind, forge, slug, key); ok {
		return data, false, statusRefusal{}, nil
	}
	started := time.Now()
	raw, live, refused, err := statusWithForgeAdmissionContext(statusOrdinaryRead(ctx, kind, forge, slug, key), forge, "", stale, fetch)
	if err != nil {
		return nil, false, refused, err
	}
	if live && !statusResourceWriteSince(ctx, kind, forge, slug, key, raw, started) {
		if err := ctx.Err(); err != nil {
			return nil, false, statusRefusalFor(err), err
		}
		if winner, ok := statusReadForgeEntryAnyContext(ctx, kind, forge, slug, key); ok {
			return winner, false, statusRefusal{}, nil
		}
		return nil, false, statusRefusal{}, errors.New("superseding forge observation is unavailable")
	}
	return raw, live, refused, ctx.Err()
}
