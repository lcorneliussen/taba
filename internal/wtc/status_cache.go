package wtc

import (
	"crypto/sha256"
	"encoding/hex"
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
	digest := sha256.Sum256([]byte(kind + "\x00" + forge + "\x00" + slug + "\x00" + key))
	return filepath.Join(statusForgeCacheDir(), kind+"-"+hex.EncodeToString(digest[:])+".json")
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
	return statusReadForgeEntry("pr", forge, slug, number)
}

func statusReadForgeEntry(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryWithAge(kind, forge, slug, key, statusForgeCacheAge())
}

func statusReadForgeEntryAny(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryWithAge(kind, forge, slug, key, 365*24*time.Hour)
}

func statusReadForgeEntryWithAge(kind, forge, slug, key string, age time.Duration) ([]byte, bool) {
	data, _, ok := statusReadForgeEntryObserved(kind, forge, slug, key, age)
	return data, ok
}

// Read bytes and observation time from the same opened regular file, even
// while another pane atomically publishes a newer observation.
func statusReadForgeEntryObserved(kind, forge, slug, key string, age time.Duration) ([]byte, time.Time, bool) {
	path := statusForgeEntryPath(kind, forge, slug, key)
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
	return data, info.ModTime(), err == nil && len(data) != 0
}

func statusWriteForgeCache(forge, slug, number string, data []byte) {
	statusWriteForgeEntry("pr", forge, slug, number, data)
}

// statusWriteForgeEntrySince publishes data fetched by a request that started
// at started, unless the record already holds a response from a later request.
// Within statusMergedCacheAge, a retained MERGED record rejects active replies;
// a first confirmed merge can supersede a later active response.
// Callers hold the record lock when it can be taken; without it two writers can
// still interleave between the check and the write.
func statusWriteForgeEntrySince(kind, forge, slug, key string, data []byte, started time.Time) bool {
	return statusWriteForgeEntrySinceObserved(kind, forge, slug, key, data, started, time.Time{})
}

// List-backed detail carries the list's observation time through atomic
// publication, so later cache reads cannot mistake publication for a new read.
func statusWriteForgeEntrySinceObserved(kind, forge, slug, key string, data []byte, started, observed time.Time) bool {
	if len(data) == 0 {
		return false
	}
	promotesMerge := false
	if kind == "pr" {
		candidate, parseErr := statusParseForgeDetail(forge, data, PRRecord{Number: key})
		current, known := statusReadObservedDetail(PRRecord{Number: key}, slug, forge, statusMergedCacheAge())
		if known && current.State == "MERGED" && (parseErr != nil || candidate.State != "MERGED") {
			return false
		}
		promotesMerge = parseErr == nil && candidate.State == "MERGED" && (!known || current.State != "MERGED")
	}
	// A first confirmed merge supersedes an active-list observation. Within
	// the same state, request-start provenance still selects the winning facts.
	if saved, ok := statusForgeEntrySince(kind, forge, slug, key); ok && saved.After(started) && !promotesMerge {
		return false
	}
	if !statusWriteForgeEntryObserved(kind, forge, slug, key, data, observed) {
		return false
	}
	statusWriteForgeEntry("since", forge, slug, kind+":"+key, []byte(strconv.FormatInt(started.UnixNano(), 10)))
	return true
}

func statusForgeEntrySince(kind, forge, slug, key string) (time.Time, bool) {
	saved, ok := statusReadForgeEntryAny("since", forge, slug, kind+":"+key)
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
	if len(data) == 0 {
		return false
	}
	path := statusForgeEntryPath(kind, forge, slug, key)
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
	return os.Rename(tmp.Name(), path) == nil
}
