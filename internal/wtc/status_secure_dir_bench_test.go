package wtc

import (
	"os"
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
	config := b.TempDir()
	b.Setenv("GH_CONFIG_DIR", config)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		b.Setenv(name, "")
	}
	if err := os.WriteFile(filepath.Join(config, "hosts.yml"), []byte("github.com:\n    user: someone\n    git_protocol: https\n"), 0600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.yml"), []byte("version: \"1\"\ngit_protocol: https\n"), 0600); err != nil {
		b.Fatal(err)
	}
	if !statusResolveAuthContext("github.com").known {
		b.Fatal("synthetic gh login was not a known context")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		statusResolveAuthContext("github.com")
	}
}
