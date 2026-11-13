package wtc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretLinksRespectIgnoreBackupProdAndDryRun(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	harness := filepath.Join(collection, "harness")
	repo := filepath.Join(collection, "app")
	control := filepath.Join(realPath(t, filepath.Dir(collection)), ".config")
	for _, dir := range []string{harness, repo, filepath.Join(control, "app", "config")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://github.com/example/app.git\n    prod_paths: [separate.prod]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, "wtc.toml"), []byte("[secrets]\nprod_paths = ['app/.env.prod']\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, ".env.collection"), []byte("WTC_CONFIG_ROOT='"+control+"'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".env*\n*.prod\nconfig/credentials.json\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{".env": "new", ".env.prod": "production", "separate.prod": "production", "config/credentials.json": "config", "public.txt": "refuse"} {
		if err := os.WriteFile(filepath.Join(control, "app", path), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_CONFIG_ROOT", "")
	c, err := OpenCollection(collection)
	if err != nil || c.ConfigRoot != control {
		t.Fatalf("generated collection control root: %q %v", c.ConfigRoot, err)
	}
	dry, err := c.LinkSecrets(SecretLinkOptions{DryRun: true})
	if err == nil || dry.Linked != 2 || dry.Refused != 1 || dry.ProdSkipped != 2 || dry.BackedUp != 0 {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	if body, err := os.ReadFile(filepath.Join(repo, ".env")); err != nil || string(body) != "old" {
		t.Fatalf("dry run changed existing file: %q %v", body, err)
	}
	linked, err := c.LinkSecrets(SecretLinkOptions{})
	if err == nil || linked.Linked != 2 || linked.Refused != 1 || linked.ProdSkipped != 2 || linked.BackedUp != 1 {
		t.Fatalf("link: %+v %v", linked, err)
	}
	if target, err := os.Readlink(filepath.Join(repo, ".env")); err != nil || target != filepath.Join(control, "app", ".env") {
		t.Fatalf("wrong secret link: %q %v", target, err)
	}
	backups, err := filepath.Glob(filepath.Join(collection, ".harness-backups", "app", ".env.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected preserved copy: %v %v", backups, err)
	}
	if body, err := os.ReadFile(backups[0]); err != nil || string(body) != "old" {
		t.Fatalf("backup content: %q %v", body, err)
	}
	again, err := c.LinkSecrets(SecretLinkOptions{IncludeProd: true})
	if err == nil || again.Current != 2 || again.Linked != 2 || again.ProdSkipped != 0 {
		t.Fatalf("idempotence and prod opt-in: %+v %v", again, err)
	}
	if _, err := os.Readlink(filepath.Join(repo, ".env.prod")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(repo, "separate.prod")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(repo, "public.txt")); !os.IsNotExist(err) {
		t.Fatalf("unignored file was linked: %v", err)
	}
}

func TestSecretLinksRejectParentSymlink(t *testing.T) {
	collection := filepath.Join(t.TempDir(), "sample")
	harness := filepath.Join(collection, "harness")
	repo := filepath.Join(collection, "app")
	control := filepath.Join(realPath(t, filepath.Dir(collection)), ".config")
	for _, dir := range []string{harness, repo, filepath.Join(control, "app", "config")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(harness, ".harness-repos.yml"), []byte("repos:\n  - name: app\n    remote: https://github.com/example/app.git\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("config/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "app", "config", "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "config")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_CONFIG_ROOT", control)
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.LinkSecrets(SecretLinkOptions{}); err == nil || !strings.Contains(err.Error(), "unsafe parent") {
		t.Fatalf("did not reject symlink escape: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); !os.IsNotExist(err) {
		t.Fatalf("secret escaped the worktree: %v", err)
	}
}
