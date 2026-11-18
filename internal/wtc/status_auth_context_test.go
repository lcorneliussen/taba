package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func auth441Fixture(t *testing.T) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_CONFIG_DIR", "GH_HOST", "GH_REPO", "XDG_CONFIG_HOME", "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
}
func TestStatusAuth441ContextBoundary(t *testing.T) {
	auth441Fixture(t)
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			if statusResolveAuthContext(forge).known {
				t.Fatal("logout established credential context")
			}
			user, token := "BB_USERNAME", "BB_API_TOKEN"
			if forge == "github.com" {
				user, token = "GH_REPO", "GH_TOKEN"
			}
			t.Setenv(user, "synthetic-user")
			t.Setenv(token, "synthetic-permission-A")
			a := statusResolveAuthContext(forge)
			if !a.known {
				t.Fatal("synthetic credential not established")
			}
			if a != statusResolveAuthContext(forge) {
				t.Fatal("unchanged context did not coalesce")
			}
			t.Setenv(token, "synthetic-permission-B")
			b := statusResolveAuthContext(forge)
			if !b.known || a.scope == b.scope {
				t.Fatal("same-user credential rotation shared scope")
			}
			t.Setenv(user, "synthetic-other-user")
			if b.scope == statusResolveAuthContext(forge).scope {
				t.Fatal("account selection shared scope")
			}
			for _, kind := range []string{"pr", "merge", "openprs", "branch", "activity", "checks", "checkhead", "build", "pipelines", "since", "full", "omit", "stamp"} {
				path := auth441Digest(kind, forge, "example/widget", "7")
				if strings.Contains(path, "synthetic") || len(path) != 64 {
					t.Fatal("nonopaque cache identity")
				}
			}
			t.Setenv(token, "")
			if statusResolveAuthContext(forge).known {
				t.Fatal("logout retained authority")
			}
		})
	}
}
func TestStatusAuth441StoreAndLegacyBoundary(t *testing.T) {
	auth441Fixture(t)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)
	path := filepath.Join(dir, "hosts.yml")
	fixtureFile(t, path, "github.com:\n  user: synthetic\n", 0600)
	if statusResolveAuthContext("github.com").known {
		t.Fatal("login name equated keychain credentials")
	}
	fixtureFile(t, path, "github.com:\n  user: synthetic\n  oauth_token: synthetic-A\n", 0600)
	a := statusResolveAuthContext("github.com")
	if !a.known {
		t.Fatal("local selected token unknown")
	}
	fixtureFile(t, path, "github.com:\n  user: synthetic\n  oauth_token: synthetic-B\n", 0600)
	if a.scope == statusResolveAuthContext("github.com").scope {
		t.Fatal("file rotation not invalidated")
	}
	other := t.TempDir()
	fixtureFile(t, filepath.Join(other, "hosts.yml"), "github.com:\n  user: synthetic\n  oauth_token: synthetic-A\n", 0600)
	t.Setenv("GH_CONFIG_DIR", other)
	if a.scope == statusResolveAuthContext("github.com").scope {
		t.Fatal("store change shared scope")
	}
	for _, kind := range []string{"pr", "merge", "checks", "build", "openprs", "since", "full"} {
		old := sha256.Sum256([]byte(kind + "\x00github.com\x00example/widget\x007"))
		if auth441Digest(kind, "github.com", "example/widget", "7") == hex.EncodeToString(old[:]) {
			t.Fatal("legacy cache promoted")
		}
	}
	for _, kind := range []string{"budget", "cool", "pending"} {
		old := sha256.Sum256([]byte(kind + "\x00github.com\x00\x00"))
		if auth441Digest(kind, "github.com", "", "") != hex.EncodeToString(old[:]) {
			t.Fatal("released shared control path changed")
		}
	}
}
func TestStatusAuth441KeyLossAndUnsafeKey(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	before := statusResolveAuthContext("github.com")
	path := filepath.Join(statusForgeCacheDir(), ".auth-context-key-v1")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	after := statusResolveAuthContext("github.com")
	if !before.known || !after.known || before.scope == after.scope {
		t.Fatal("cache key loss did not invalidate")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unrelated")
	fixtureFile(t, target, strings.Repeat("x", 32), 0600)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if statusResolveAuthContext("github.com").known {
		t.Fatal("symlink cache key accepted")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != strings.Repeat("x", 32) {
		t.Fatal("unrelated file changed")
	}
}

func TestStatusAuth441ActualCollectorCrossover(t *testing.T) {
	auth441Fixture(t)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
case "$GH_TOKEN" in
 synthetic-A) title="Context A";;
 synthetic-B) title="Context B";;
 *) exit 1;;
esac
printf '{"number":7,"state":"OPEN","title":"%s"}\n' "$title"
`, 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "synthetic-A")
	a := statusViewPRWithLiveContext(context.Background(), PRRecord{Number: "7"}, "example/widget", "github.com", false, false, nil)
	t.Setenv("GH_TOKEN", "synthetic-B")
	b := statusViewPRWithLiveContext(context.Background(), PRRecord{Number: "7"}, "example/widget", "github.com", false, false, nil)
	if a.Title != "Context A" || b.Title != "Context B" {
		t.Fatalf("collector crossed credential contexts: A=%q B=%q", a.Title, b.Title)
	}
}
func TestStatusAuth441InflightAndLatePublication(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	ctx := statusAuthOperation(context.Background())
	var calls atomic.Int32
	fetch := func() ([]byte, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return []byte("synthetic observation"), nil
	}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := statusFetchSharedContext(ctx, "probe", "github.com", "example/widget", "7", fetch)
			done <- err
		}()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unchanged contexts did not share inflight work")
	}
	t.Setenv("GH_TOKEN", "synthetic-B")
	result, err := statusWriteForgeEntrySinceObservedContext(ctx, "probe", "github.com", "example/widget", "7", []byte("late A"), time.Now(), time.Now())
	if err == nil || result.Written {
		t.Fatal("old operation published into new context")
	}
	if _, ok := statusReadForgeEntry("probe", "github.com", "example/widget", "7"); ok {
		t.Fatal("B borrowed A observation")
	}
	_, _, err = statusFetchSharedContext(context.Background(), "probe", "github.com", "example/widget", "7", func() ([]byte, error) { return nil, errors.New("synthetic permission denied") })
	if err == nil {
		t.Fatal("denied B borrowed A instead of failing")
	}
}

func auth441Digest(kind, forge, slug, key string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(statusForgeEntryPath(kind, forge, slug, key)), kind+"-"), ".json")
}

func TestStatusAuth441AllObservationKindsAndLegacy(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	for _, kind := range []string{"pr", "merge", "openprs", "checks", "checkhead", "pipelines", "build", "branch", "activity", "since", "full", "omit", "stamp"} {
		statusWriteForgeEntry(kind, "github.com", "example/widget", "7", []byte("synthetic A observation"))
	}
	t.Setenv("GH_TOKEN", "synthetic-B")
	for _, kind := range []string{"pr", "merge", "openprs", "checks", "checkhead", "pipelines", "build", "branch", "activity", "since", "full", "omit", "stamp"} {
		if _, ok := statusReadForgeEntryAny(kind, "github.com", "example/widget", "7"); ok {
			t.Fatalf("%s borrowed A", kind)
		}
	}
	old := sha256.Sum256([]byte("pr\x00github.com\x00example/widget\x007"))
	fixtureFile(t, filepath.Join(statusForgeCacheDir(), "pr-"+hex.EncodeToString(old[:])+".json"), `{"number":7,"state":"MERGED","title":"Legacy"}`, 0600)
	if _, ok := statusRetainedMerge(PRRecord{Number: "7"}, "example/widget", "github.com"); ok {
		t.Fatal("legacy merge promoted")
	}
	t.Setenv("GH_TOKEN", "")
	if _, ok := statusReadForgeEntryAny("pr", "github.com", "example/widget", "7"); ok {
		t.Fatal("unknown borrowed legacy")
	}
	if statusWriteForgeEntryObserved("pr", "github.com", "example/widget", "7", []byte("unknown"), time.Now()) {
		t.Fatal("unknown persisted reusable observation")
	}
}

func TestStatusAuth441SnapshotFallbackAndPresentation(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	c := &Context{Collection: t.TempDir()}
	ctx := statusAuthOperation(context.Background())
	url := "https://github.com/example/widget/pull/7"
	a := StatusSnapshot{Schema: 1, Collection: filepath.Base(c.Collection), authContext: statusAuthSnapshotScope(ctx), PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "MERGED", Title: "Context A"}}}
	if err := c.writeStatusSnapshotContext(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if saved, err := c.ReadScopedStatusSnapshot(); err != nil || saved.PRs[0].Title != "Context A" {
		t.Fatal("same-context active fallback unavailable", err)
	}
	t.Setenv("GH_TOKEN", "synthetic-B")
	if _, err := c.ReadScopedStatusSnapshot(); err == nil {
		t.Fatal("B active fallback borrowed A snapshot")
	}
	// Offline preview remains available by explicit request, without a scope stamp.
	offline, err := c.ReadStatusSnapshot()
	if err != nil || offline.authContext != "" {
		t.Fatal("offline preview gained active provenance", err)
	}
	b := a
	b.authContext = statusAuthSnapshotScope(context.Background())
	b.PRs = []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "UNKNOWN"}}
	if got := StatusPreferNewerObservations(b, a); got.PRs[0].Title != "" || got.PRs[0].State != "UNKNOWN" {
		t.Fatal("presentation retained another context's merge")
	}
	if err := c.writeStatusSnapshotContext(ctx, a, nil); err == nil {
		t.Fatal("old operation published snapshot after selection changed")
	}
}
