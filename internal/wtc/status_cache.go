package wtc

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

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

func statusReadMergedForgeCache(forge, slug, number string) ([]byte, bool) {
	return statusReadForgeEntryWithAge("pr", forge, slug, number, statusMergedCacheAge())
}

func statusReadForgeEntry(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryWithAge(kind, forge, slug, key, statusForgeCacheAge())
}

func statusReadForgeEntryAny(kind, forge, slug, key string) ([]byte, bool) {
	return statusReadForgeEntryWithAge(kind, forge, slug, key, 365*24*time.Hour)
}

func statusReadForgeEntryWithAge(kind, forge, slug, key string, age time.Duration) ([]byte, bool) {
	path := statusForgeEntryPath(kind, forge, slug, key)
	if !statusSecureCacheDir(filepath.Dir(path)) {
		return nil, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) >= age {
		return nil, false
	}
	data, err := os.ReadFile(path)
	return data, err == nil && len(data) != 0
}

func statusWriteForgeCache(forge, slug, number string, data []byte) {
	statusWriteForgeEntry("pr", forge, slug, number, data)
}

// statusWriteForgeEntrySince publishes data fetched by a request that started
// at started, unless the record already holds a response from a later request.
// Callers hold the record lock when it can be taken; without it two writers can
// still interleave between the check and the write.
func statusWriteForgeEntrySince(kind, forge, slug, key string, data []byte, started time.Time) bool {
	if len(data) == 0 {
		return false
	}
	if saved, ok := statusForgeEntrySince(kind, forge, slug, key); ok && saved.After(started) {
		return false
	}
	statusWriteForgeEntry(kind, forge, slug, key, data)
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
	if len(data) == 0 {
		return
	}
	path := statusForgeEntryPath(kind, forge, slug, key)
	if !statusSecureCacheDir(filepath.Dir(path)) {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pr-")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if tmp.Chmod(0600) != nil {
		tmp.Close()
		return
	}
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if tmp.Close() != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}
