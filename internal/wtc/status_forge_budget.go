package wtc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	if !statusWriteForgeEntryObserved("cool", forge, "", "", []byte(strconv.FormatInt(until.UnixNano(), 10)), time.Time{}) {
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
	if ctx.Err() != nil {
		return false
	}
	burst, every := statusForgeAllowance(forge)
	if burst >= 1<<30 {
		return true
	}
	path := statusForgeStatePath("budget", forge)
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return false
	}
	defer unlock()
	now := time.Now()
	queue := statusReadPendingConfirmations(forge, now)
	if forge != "bitbucket.org" {
		// GitHub has no legacy confirmation FIFO. Only an owned foreground lease
		// may defer its ordinary reads; unrelated queue bytes remain untouched.
		for _, item := range queue {
			if item.Foreground {
				return false
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
		return false
	}
	tokens, updated := statusReadForgeBudget(path, burst, every, now)
	// Released clients honor this existing lock but do not know the FIFO.
	// Keep its next refill for the head confirmation, bounded by the standard
	// Bitbucket interval. Provider I/O starts only after this lock is released.
	if forge == "bitbucket.org" && confirmation != "" && admitted && tokens == 0 && !statusForgeCooling(forge) {
		delay := updated.Add(every).Sub(now)
		if delay > 0 && delay <= statusBitbucketEvery {
			saveQueue()
			// Wait at most statusBitbucketEvery while holding the budget lock;
			// provider I/O must stay outside this wait.
			if statusWaitRefresh(ctx, delay) != nil {
				return false
			}
			now = time.Now()
			tokens, updated = statusReadForgeBudget(path, burst, every, now)
			if statusForgeCooling(forge) {
				return false
			}
		}
	}
	// Keep one token per queued confirmation; spare burst capacity remains
	// available to ordinary reads while confirmations retain FIFO order.
	if confirmation == "" && tokens > len(queue) {
		admitted = true
	}
	if tokens < 1 || !admitted {
		saveQueue()
		return false
	}
	if confirmation != "" && len(queue) > 0 && queue[0].Key == confirmation {
		queue = queue[1:]
	}
	if ctx.Err() != nil {
		return false
	}
	saveQueue()
	tokens--
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens, updated.UnixNano())), 0600) == nil
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
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// GitHub identity recovery already schedules its ordinary and repair reads.
	// Reserve admission only for Bitbucket omission confirmations.
	if forge != "bitbucket.org" {
		confirmation = ""
	}
	if statusForgeCooling(forge) {
		if len(stale) > 0 {
			return stale, false, nil
		}
		return nil, false, errForgeCooling
	}
	if !statusTakeForgeTokenForContext(ctx, forge, statusConfirmationKey(confirmation)) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(stale) > 0 {
			return stale, false, nil
		}
		return nil, false, errForgeBudget
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	raw, err := fetch()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if err != nil {
		if statusRateLimited(err) {
			statusForgeCoolContext(ctx, forge, statusRetryAfter(err))
			if len(stale) > 0 {
				return stale, false, nil
			}
		}
		return nil, false, err
	}
	return raw, true, nil
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
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if data, ok := statusReadForgeEntry(kind, forge, slug, key); ok {
		return data, false, nil
	}
	stale, _ := statusReadForgeEntryAny(kind, forge, slug, key)
	unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPath(kind, forge, slug, key))
	if lockErr != nil {
		return nil, false, lockErr
	}
	defer unlock()
	if data, ok := statusReadForgeEntry(kind, forge, slug, key); ok {
		return data, false, nil
	}
	started := time.Now()
	raw, live, err := statusWithForgeBudgetKeyContext(ctx, forge, "", stale, fetch)
	if err != nil {
		return nil, false, err
	}
	if live && !statusResourceWriteSince(ctx, kind, forge, slug, key, raw, started) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if winner, ok := statusReadForgeEntryAny(kind, forge, slug, key); ok {
			return winner, false, nil
		}
		return nil, false, errors.New("superseding forge observation is unavailable")
	}
	return raw, live, ctx.Err()
}
