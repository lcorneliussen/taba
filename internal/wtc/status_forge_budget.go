package wtc

import (
	"context"
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
// record, and one token bucket per forge keep the panes from spending the
// hour on the first few minutes. Every status provider read takes one token;
// a background read without one is refused, and an explicit refresh waits for
// the next refill. Nothing is queued or reserved. A rate-limit response pauses that
// forge for its Retry-After and status keeps showing the last saved record.

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

// statusForgeNow is the clock of the shared bucket and cooldown; tests drive it.
var statusForgeNow = time.Now

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
	until := statusForgeNow().Add(retry)
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

// statusTakeForgeToken debits one token from forge's shared bucket. It
// returns nil when admitted and a *statusAdmissionError, with the retry time,
// while the forge cools down or the bucket is empty. A cancelled context or a
// failed lock admits nothing and is returned as is.
func statusTakeForgeToken(ctx context.Context, forge string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if until := statusForgeCoolingUntil(forge); statusForgeNow().Before(until) {
		return &statusAdmissionError{cause: errForgeCooling, retry: until}
	}
	burst, every := statusForgeAllowance(forge)
	if burst >= 1<<30 {
		return nil
	}
	path := statusForgeStatePath("budget", forge)
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	tokens, updated := statusReadForgeBudget(path, burst, every, statusForgeNow())
	if tokens < 1 {
		return &statusAdmissionError{cause: errForgeBudget, retry: updated.Add(time.Duration(1-tokens) * every)}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens-1, updated.UnixNano())), 0600)
}

// statusTakeForegroundToken admits an explicit refresh from the same bucket.
// When the bucket is empty it waits for the next refill, at most one standard
// provider interval at a time, until its request deadline. It holds no
// reservation: a background read can take that refill first.
func statusTakeForegroundToken(ctx context.Context, forge string) error {
	ctx, cancel := statusRefreshContext(ctx)
	defer cancel()
	if forge != "github.com" && forge != "bitbucket.org" {
		return errors.New("unsupported forge")
	}
	limit := statusGitHubEvery
	if forge == "bitbucket.org" {
		limit = statusBitbucketEvery
	}
	for {
		err := statusTakeForgeToken(ctx, forge)
		var refused *statusAdmissionError
		if !errors.Is(err, errForgeBudget) || !errors.As(err, &refused) {
			return err
		}
		// A custom interval longer than a provider refill defers honestly
		// instead of waiting minutes.
		delay := refused.retry.Sub(statusForgeNow())
		if delay > limit {
			return err
		}
		if err := statusWaitRefresh(ctx, max(0, delay)); err != nil {
			return err
		}
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

// Context-aware acquisition keeps cancellation finite while another process
// holds the lock. A failed lock is never permission for an unaccounted
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

// Caller holds the budget lock. The file keeps the two-field format
// "<tokens> <refilled-unix-nanos>" that released clients read and write under
// the same lock.
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
	body, live, _, err = statusWithForgeAdmissionContext(context.Background(), StatusRefreshBackground, forge, stale, fetch)
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

// An explicit refresh waits for the next refill (statusTakeForegroundToken);
// background reads take a token or are refused.
func statusWithForgeAdmissionContext(ctx context.Context, intent StatusRefreshIntent, forge string, stale []byte, fetch func() ([]byte, error)) (body []byte, live bool, refused statusRefusal, err error) {
	ctx = statusAuthOperation(ctx)
	selected := statusAuthForContext(ctx, forge)
	take := statusTakeForgeToken
	if intent == StatusRefreshForeground {
		take = statusTakeForegroundToken
	}
	if err := take(ctx, forge); err != nil {
		if ctx.Err() != nil {
			return nil, false, statusRefusalFor(ctx.Err()), ctx.Err()
		}
		refused = statusRefusalFor(err)
		if refused.reason == "" {
			// A budget that cannot be accounted (an unsafe lock) admits nothing.
			refused.reason, err = "shared-budget", errForgeBudget
		}
		if len(stale) > 0 {
			return stale, false, refused, nil
		}
		return nil, false, refused, err
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
	raw, live, refused, err := statusWithForgeAdmissionContext(ctx, StatusRefreshBackground, forge, stale, fetch)
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
