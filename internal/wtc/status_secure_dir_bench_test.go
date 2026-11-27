package wtc

import (
	"path/filepath"
	"testing"
)

func BenchmarkStatusSecureCacheDirExisting(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "cache")
	if !statusSecureCacheDir(dir) {
		b.Fatal("setup")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !statusSecureCacheDir(dir) {
			b.Fatal("rejected")
		}
	}
}

func BenchmarkStatusResolveAuthContextGitHub(b *testing.B) {
	b.Setenv("TMPDIR", b.TempDir())
	b.Setenv("GH_CONFIG_DIR", b.TempDir())
	for i := 0; i < b.N; i++ {
		statusResolveAuthContext("github.com")
	}
}
