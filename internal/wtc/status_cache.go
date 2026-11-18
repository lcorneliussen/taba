package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
	"taba.sh/taba/internal/envcompat"
)

func statusForgeCacheAge() time.Duration {
	seconds, err := strconv.Atoi(envcompat.Getenv("WTC_FORGE_CACHE_AGE"))
	if err != nil || seconds < 1 {
		seconds = 90
	}
	return time.Duration(seconds) * time.Second
}

func statusMergedCacheAge() time.Duration {
	// Explicit cache-age overrides apply to both active and merged PRs.
	if envcompat.Getenv("WTC_FORGE_CACHE_AGE") != "" {
		return statusForgeCacheAge()
	}
	return 24 * time.Hour
}

func statusForgeCacheDir() string {
	return filepath.Join(os.TempDir(), "wtc-status-"+strconv.Itoa(os.Getuid()))
}

func statusForgeCachePath(forge, slug, number string) string {
	return statusForgeEntryPath("pr", forge, slug, number)
}

func statusForgeEntryPath(kind, forge, slug, key string) string {
	return statusForgeEntryPathContext(context.Background(), kind, forge, slug, key)
}
func statusForgeEntryPathContext(ctx context.Context, kind, forge, slug, key string) string {
	input := kind + "\x00" + forge + "\x00" + slug + "\x00" + key
	if !statusAuthSharedKind(kind) {
		scope := statusAuthForContext(ctx, forge).scope
		if !statusAuthForContext(ctx, forge).known {
			if state, ok := ctx.Value(statusAuthOperationKey{}).(*statusAuthOperationState); ok {
				scope += "\x00operation:" + state.nonce
			}
		}
		input = "auth-v1\x00" + scope + "\x00" + input
	}
	digest := sha256.Sum256([]byte(input))
	path := filepath.Join(statusForgeCacheDir(), kind+"-"+hex.EncodeToString(digest[:])+".json")
	if !statusAuthSharedKind(kind) && !statusAuthForContext(ctx, forge).known {
		statusAuthMemoryPath(ctx, path)
	}
	return path
}

func statusSecureCacheDir(path string) bool {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if info.Mode().Perm() != 0700 && os.Chmod(path, 0700) != nil {
		return false
	}
	return true
}

func statusReadForgeCache(forge, slug, number string) ([]byte, bool) {
	return statusReadForgeCacheContext(context.Background(), forge, slug, number)
}

func statusReadForgeCacheContext(ctx context.Context, forge, slug, number string) ([]byte, bool) {
	ctx = statusAuthOperation(ctx)
	return statusReadForgeEntryContext(ctx, "pr", forge, slug, number)
}

func statusReadForgeEntry(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryContext(context.Background(), kind, forge, slug, key)
}

func statusReadForgeEntryContext(ctx context.Context, kind, forge, slug, key string) ([]byte, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	return statusReadForgeEntryWithAgeContext(ctx, kind, forge, slug, key, statusForgeCacheAge())
}

func statusReadForgeEntryAny(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryAnyContext(context.Background(), kind, forge, slug, key)
}

func statusReadForgeEntryAnyContext(ctx context.Context, kind, forge, slug, key string) ([]byte, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	return statusReadForgeEntryWithAgeContext(ctx, kind, forge, slug, key, 365*24*time.Hour)
}

func statusReadForgeEntryWithAge(kind, forge, slug, key string, age time.Duration) ([]byte, bool) {
	return statusReadForgeEntryWithAgeContext(context.Background(), kind, forge, slug, key, age)
}

func statusReadForgeEntryWithAgeContext(ctx context.Context, kind, forge, slug, key string, age time.Duration) ([]byte, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	data, _, ok := statusReadForgeEntryObservedContext(ctx, kind, forge, slug, key, age)
	return data, ok
}

// Read bytes and observation time from the same opened regular file, even
// while another pane atomically publishes a newer observation.
func statusReadForgeEntryObserved(kind, forge, slug, key string, age time.Duration) ([]byte, time.Time, bool) {
	return statusReadForgeEntryObservedContext(context.Background(), kind, forge, slug, key, age)
}

func statusReadForgeEntryObservedContext(ctx context.Context, kind, forge, slug, key string, age time.Duration) ([]byte, time.Time, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	if !statusAuthSharedKind(kind) && !statusAuthSelectionMatches(ctx, forge) {
		return nil, time.Time{}, false
	}

	path := statusForgeEntryPathContext(ctx, kind, forge, slug, key)
	if !statusAuthSharedKind(kind) && !statusAuthForContext(ctx, forge).known {
		return statusAuthMemoryRead(ctx, path, age)
	}
	if !statusSecureCacheDir(filepath.Dir(path)) {
		return nil, time.Time{}, false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, time.Time{}, false
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) >= age {
		return nil, time.Time{}, false
	}
	data, err := io.ReadAll(file)
	return data, info.ModTime(), err == nil && len(data) != 0 && (statusAuthSharedKind(kind) || statusAuthCurrent(ctx, forge))
}

func statusWriteForgeCache(forge, slug, number string, data []byte) {
	statusWriteForgeEntry("pr", forge, slug, number, data)
}

// statusWriteForgeEntrySince publishes data fetched by a request that started
// at started, unless the record already holds a response from a later request.
// Within statusMergedCacheAge, a retained MERGED record rejects non-MERGED or
// unparseable replies. A first confirmed merge supersedes any non-MERGED or
// absent observation, regardless of request order.
// Callers hold the record lock when it can be taken; without it two writers can
// still interleave between the check and the write.
func statusWriteForgeEntrySince(kind, forge, slug, key string, data []byte, started time.Time) bool {
	return statusWriteForgeEntrySinceObserved(kind, forge, slug, key, data, started, time.Time{})
}

// List-backed detail carries the list's observation time through atomic
// publication, so later cache reads cannot mistake publication for a new read.
func statusWriteForgeEntrySinceObserved(kind, forge, slug, key string, data []byte, started, observed time.Time) bool {
	result, _ := statusWriteForgeEntrySinceObservedContext(context.Background(), kind, forge, slug, key, data, started, observed)
	return result.Written
}

// statusCachePublication separates detail persistence from terminal authority.
type statusCachePublication struct {
	Written  bool
	Retained bool
}

// A retained merge is an independently committed fact even when cancellation
// prevents the ordinary detail publication. Keep persistence separate from the
// request's outcome. Filesystem operations themselves are not universally
// cancellable; once an atomic publication finishes, its evidence is retained.
func statusWriteForgeEntrySinceObservedContext(ctx context.Context, kind, forge, slug, key string, data []byte, started, observed time.Time) (result statusCachePublication, err error) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	if !statusAuthSelectionMatches(ctx, forge) {
		return result, errors.New("forge credential context unavailable or changed")
	}

	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(data) == 0 {
		return result, nil
	}
	promotesMerge, isMerge := false, false
	if kind == "pr" {
		candidate, parseErr := statusParseForgeDetail(forge, data, PRRecord{Number: key})
		current, known, err := statusRetainedMergeContext(ctx, PRRecord{Number: key}, slug, forge)
		if err != nil {
			// Legacy migration may itself have committed retention before the
			// request ended. Preserve that authority independently of whether
			// this candidate can still be published.
			result.Retained = known
			return result, err
		}
		if known && current.State == "MERGED" && (parseErr != nil || candidate.State != "MERGED") {
			return result, nil
		}
		isMerge = parseErr == nil && candidate.State == "MERGED"
		promotesMerge = isMerge && !known
	}
	// A first confirmed MERGED observation bypasses request order. Otherwise
	// preserve ordering, including released writers using the two-file protocol.
	if saved, ok := statusForgeEntrySinceContext(ctx, kind, forge, slug, key); ok && saved.After(started) && !promotesMerge {
		return result, nil
	}
	// Released writers can replace ordinary detail, so terminal evidence is
	// retained independently; reading it never renews its observation age.
	if isMerge {
		if observed.IsZero() {
			observed = time.Now()
		}
		retained, err := statusRetainMergeObservationContext(ctx, forge, slug, key, data, observed)
		result.Retained = retained
		if err != nil || !retained {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !statusWriteForgeEntryObservedContext(ctx, kind, forge, slug, key, data, observed) {
		return result, errors.New("forge observation publication failed")
	}
	result.Written = true
	// Complete the existing ordering receipt after a successful publication,
	// even if cancellation arrived during the filesystem operation.
	statusWriteForgeEntryObservedContext(ctx, "since", forge, slug, kind+":"+key, []byte(strconv.FormatInt(started.UnixNano(), 10)), time.Time{})
	return result, ctx.Err()
}

func statusRetainMergeObservation(forge, slug, key string, data []byte, observed time.Time) bool {
	retained, _ := statusRetainMergeObservationContext(context.Background(), forge, slug, key, data, observed)
	return retained
}

// Serialize new publications and legacy migrations on the independent record.
// Observation time, not migration time, controls both ordering and expiry.
// Callers may already hold the ordinary PR lock; this path never acquires it.
func statusRetainMergeObservationContext(ctx context.Context, forge, slug, key string, data []byte, observed time.Time) (bool, error) {
	ctx = statusAuthOperation(ctx)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if observed.IsZero() || time.Since(observed) >= statusMergedCacheAge() {
		return false, nil
	}
	unlock, err := statusLockCacheContext(ctx, statusForgeEntryPathContext(ctx, "merge", forge, slug, key))
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	saved, at, ok := statusReadForgeEntryObservedContext(ctx, "merge", forge, slug, key, statusMergedCacheAge())
	if ok {
		prior, err := statusParseForgeDetail(forge, saved, PRRecord{Number: key})
		if err == nil && prior.State == "MERGED" && !observed.After(at) {
			return true, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	retained := statusWriteForgeEntryObservedContext(ctx, "merge", forge, slug, key, data, observed)
	if !retained {
		return false, errors.New("merge observation publication failed")
	}
	// Cancellation cannot revoke a confirmed merge that has been committed.
	return true, ctx.Err()
}

func statusForgeEntrySince(kind, forge, slug, key string) (time.Time, bool) {
	return statusForgeEntrySinceContext(context.Background(), kind, forge, slug, key)
}

func statusForgeEntrySinceContext(ctx context.Context, kind, forge, slug, key string) (time.Time, bool) {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	saved, ok := statusReadForgeEntryAnyContext(ctx, "since", forge, slug, kind+":"+key)
	if !ok {
		return time.Time{}, false
	}
	nanos, err := strconv.ParseInt(string(saved), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, nanos), true
}

func statusWriteForgeEntry(kind, forge, slug, key string, data []byte) {
	statusWriteForgeEntryObserved(kind, forge, slug, key, data, time.Time{})
}

func statusWriteForgeEntryObserved(kind, forge, slug, key string, data []byte, observed time.Time) bool {
	return statusWriteForgeEntryObservedContext(context.Background(), kind, forge, slug, key, data, observed)
}

func statusWriteForgeEntryObservedContext(ctx context.Context, kind, forge, slug, key string, data []byte, observed time.Time) bool {
	if !statusAuthSharedKind(kind) {
		ctx = statusAuthOperation(ctx)
	}
	// A completed data publication must finish its ordering receipt in the
	// pinned namespace even if local selection changed in between. A receipt
	// contains only request order and cannot lend provider authority elsewhere.
	if !statusAuthSharedKind(kind) && kind != "since" && !statusAuthSelectionMatches(ctx, forge) {
		return false
	}

	if len(data) == 0 {
		return false
	}
	path := statusForgeEntryPathContext(ctx, kind, forge, slug, key)
	if !statusAuthSharedKind(kind) && !statusAuthForContext(ctx, forge).known {
		return statusAuthMemoryWrite(ctx, path, data, observed)
	}
	if !statusSecureCacheDir(filepath.Dir(path)) {
		return false
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pr-")
	if err != nil {
		return false
	}
	defer os.Remove(tmp.Name())
	if tmp.Chmod(0600) != nil {
		tmp.Close()
		return false
	}
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return false
	}
	if !observed.IsZero() && os.Chtimes(tmp.Name(), observed, observed) != nil {
		tmp.Close()
		return false
	}
	if tmp.Close() != nil {
		return false
	}
	if !statusAuthSharedKind(kind) && kind != "since" && !statusAuthSelectionMatches(ctx, forge) {
		return false
	}
	return os.Rename(tmp.Name(), path) == nil
}
