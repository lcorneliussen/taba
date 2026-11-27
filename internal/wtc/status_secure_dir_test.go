package wtc

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func countCacheDirMkdirs(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	original := statusMkdirCacheDir
	statusMkdirCacheDir = func(path string, perm os.FileMode) error {
		calls.Add(1)
		return original(path, perm)
	}
	t.Cleanup(func() { statusMkdirCacheDir = original })
	return &calls
}

func TestStatusSecureCacheDir568ExistingDirectoryIsNotCreatedAgain(t *testing.T) {
	calls := countCacheDirMkdirs(t)
	dir := filepath.Join(t.TempDir(), "cache")
	if !statusSecureCacheDir(dir) {
		t.Fatal("missing directory was not created")
	}
	for i := 0; i < 100; i++ {
		if !statusSecureCacheDir(dir) {
			t.Fatal("healthy directory rejected")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("mkdir attempts = %d, want 1 (only the initial creation)", got)
	}
}

func TestStatusSecureCacheDir568RecoversRemovedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if !statusSecureCacheDir(dir) {
		t.Fatal("create")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if !statusSecureCacheDir(dir) {
		t.Fatal("removed directory was not recreated")
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("recreated directory = %v, %v", info, err)
	}
}

func TestStatusSecureCacheDir568RejectsUnsafeEntries(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"file": file, "symlink": link} {
		if statusSecureCacheDir(path) {
			t.Errorf("%s accepted as cache directory", name)
		}
	}
	if statusSecureCacheDir(filepath.Join(file, "child")) {
		t.Error("directory under a file accepted")
	}
}

func TestStatusSecureCacheDir568TightensChangedPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if !statusSecureCacheDir(dir) {
		t.Fatal("rejected")
	}
	if info, _ := os.Lstat(dir); info.Mode().Perm() != 0700 {
		t.Fatalf("mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestStatusSecureCacheDir568ConcurrentInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	var wg sync.WaitGroup
	var rejected atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !statusSecureCacheDir(dir) {
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()
	if rejected.Load() != 0 {
		t.Fatalf("%d concurrent initializers rejected the directory", rejected.Load())
	}
}

func TestStatusSecureCacheDir568AcceptsDirectoryCreatedByAnotherProcess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	original := statusMkdirCacheDir
	statusMkdirCacheDir = func(path string, perm os.FileMode) error {
		if err := original(path, perm); err != nil {
			return err
		}
		return original(path, perm) // another process created it first: EEXIST
	}
	t.Cleanup(func() { statusMkdirCacheDir = original })
	if !statusSecureCacheDir(dir) {
		t.Fatal("directory created between Lstat and mkdir was rejected")
	}
}
