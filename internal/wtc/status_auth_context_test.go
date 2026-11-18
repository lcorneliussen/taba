package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_CONFIG_DIR", "GH_HOST", "GH_REPO", "XDG_CONFIG_HOME", "BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
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
	statusWriteForgeEntryObserved("pr", "github.com", "example/widget", "7", []byte("unknown"), time.Now())
	if _, ok := statusReadForgeEntryAny("pr", "github.com", "example/widget", "7"); ok {
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

func TestStatusAuth441ProcessIsolation(t *testing.T) {
	if label := os.Getenv("WTC_TEST_441_CHILD"); label != "" {
		t.Setenv("HOME", os.Getenv("WTC_TEST_441_HOME"))
		t.Setenv("GH_CONFIG_DIR", filepath.Join(os.Getenv("HOME"), "gh"))
		t.Setenv("GH_TOKEN", "synthetic-context-"+label)
		dir := os.Getenv("WTC_TEST_441_SYNC")
		raw, _, err := statusFetchSharedContext(context.Background(), "probe", "github.com", "example/widget", "7", func() ([]byte, error) {
			if err := os.WriteFile(filepath.Join(dir, "entered-"+label), nil, 0600); err != nil {
				return nil, err
			}
			if label == "A" {
				deadline := time.Now().Add(4 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "release-A")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						return nil, fmt.Errorf("fixture barrier timed out")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			return []byte("Observation " + label), nil
		})
		if err != nil || string(raw) != "Observation "+label {
			t.Fatalf("cross-process context mismatch: %q %v", raw, err)
		}
		return
	}
	auth441Fixture(t)
	dir := t.TempDir()
	home := os.Getenv("HOME")
	child := func(label string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestStatusAuth441ProcessIsolation$")
		cmd.Env = append(os.Environ(), "WTC_TEST_441_CHILD="+label, "WTC_TEST_441_HOME="+home, "WTC_TEST_441_SYNC="+dir)
		return cmd
	}
	a := child("A")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { os.WriteFile(filepath.Join(dir, "release-A"), nil, 0600); a.Wait() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "entered-A")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("A did not enter provider fixture")
		}
		time.Sleep(5 * time.Millisecond)
	}
	b := child("B")
	done := make(chan error, 1)
	go func() {
		out, err := b.CombinedOutput()
		if err != nil {
			err = fmt.Errorf("B: %w: %s", err, out)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		b.Process.Kill()
		<-done
		t.Fatal("B queued behind A's inflight record")
	}
	os.WriteFile(filepath.Join(dir, "release-A"), nil, 0600)
	if err := a.Wait(); err != nil {
		t.Fatal(err)
	}
	// A second process with unchanged inputs must reuse the first observation.
	os.Remove(filepath.Join(dir, "entered-A"))
	if out, err := child("A").CombinedOutput(); err != nil {
		t.Fatalf("unchanged A: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "entered-A")); !os.IsNotExist(err) {
		t.Fatal("unchanged process repeated provider work")
	}
}

func TestStatusAuth441KeychainOnlyLiveAvailabilityAndCost(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("WTC_FORGE_FULL_REFRESH", "")
	home := os.Getenv("HOME")
	fixtureFile(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  user: synthetic-keychain-account\n", 0600)
	bin := t.TempDir()
	counter := filepath.Join(bin, "calls")
	t.Setenv("WTC_TEST_441_CALLS", counter)
	fixtureFile(t, filepath.Join(bin, "gh"), `#!/bin/sh
if [ "$1" = auth ]; then exit 9; fi
printf x >> "$WTC_TEST_441_CALLS"
if [ "$2" = list ]; then
 printf '%s\n' '[{"number":7,"state":"OPEN","title":"Live seven","headRefName":"topic","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"number":8,"state":"OPEN","title":"Live eight","headRefName":"other","headRefOid":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]'
else
 printf '%s\n' '{"number":7,"state":"OPEN","title":"Live seven","headRefOid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
fi
`, 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	ctx := statusAuthOperation(context.Background())
	if statusAuthForContext(ctx, "github.com").known {
		t.Fatal("keychain credential identity falsely established")
	}
	for _, number := range []string{"7", "8"} {
		detail := statusEnrichRecordMetadataContext(ctx, PRRecord{Number: number}, "example/widget", "github.com")
		if detail.State != "OPEN" || detail.ObservedAt == "" || !strings.HasPrefix(detail.Title, "Live ") {
			t.Fatalf("keychain-only live status unavailable: %+v", detail)
		}
	}
	calls, _ := os.ReadFile(counter)
	if len(calls) != 1 {
		t.Fatalf("one operation repeated list work: %d", len(calls))
	}
	next := statusAuthOperation(context.Background())
	detail := statusEnrichRecordMetadataContext(next, PRRecord{Number: "7"}, "example/widget", "github.com")
	if detail.State != "OPEN" {
		t.Fatal("next keychain-only refresh nonfunctional")
	}
	calls, _ = os.ReadFile(counter)
	if len(calls) != 2 {
		t.Fatalf("another operation reused unknown credential observations: %d", len(calls))
	}
	detail, observation := statusRefreshRecord(statusAuthOperation(context.Background()), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "github.com")
	if detail.State != "OPEN" || observation.Freshness != "live" || !observation.ProviderConfirmed {
		t.Fatalf("unknown foreground unavailable: %+v %+v", detail, observation)
	}
	files, _ := os.ReadDir(statusForgeCacheDir())
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "pr-") || strings.HasPrefix(file.Name(), "openprs-") || strings.HasPrefix(file.Name(), "merge-") {
			t.Fatalf("unknown observation escaped operation memory: %s", file.Name())
		}
	}
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	statusForgeCool("github.com", time.Hour)
	_, _, err := statusFetchSharedContext(statusAuthOperation(context.Background()), "probe", "github.com", "example/widget", "7", func() ([]byte, error) { t.Fatal("unknown bypassed shared cooldown"); return nil, nil })
	if !errors.Is(err, errForgeCooling) {
		t.Fatalf("missing truthful cooldown: %v", err)
	}
}

func TestStatusAuth441ChangedDeliveryDropsProviderKeepsLocalRecord(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	ctx := statusAuthOperation(context.Background())
	snapshot := StatusSnapshot{authContext: statusAuthSnapshotScope(ctx), authInputs: statusAuthSnapshotScope(ctx), Collection: "fixture", PRs: []StatusPRRow{{Number: "7", Title: "Provider A", State: "OPEN", StateSource: "detail-cache"}, {Number: "8", Title: "Recorded merge", State: "MERGED", StateSource: "registry"}}, Repos: []StatusRepo{{Repo: "widget", Branch: "topic", Tree: "clean", PR: &StatusPRFacts{Number: "7", StateSource: "detail-cache"}}}}
	t.Setenv("GH_TOKEN", "synthetic-B")
	if StatusSnapshotAuthCurrent(snapshot) {
		t.Fatal("late A delivery accepted after switch")
	}
	clean, changed := StatusWithoutChangedAuth(snapshot)
	if !changed || len(clean.PRs) != 1 || clean.PRs[0].Number != "8" || clean.Repos[0].PR != nil || clean.Repos[0].Branch != "topic" || clean.Repos[0].Tree != "clean" {
		t.Fatalf("provider/local boundary lost: %+v", clean)
	}
	if snapshot.Repos[0].PR == nil || len(snapshot.PRs) != 2 {
		t.Fatal("validation mutated an independent snapshot")
	}
}

func TestStatusAuth441BitbucketBasicCLIStore(t *testing.T) {
	auth441Fixture(t)
	config := filepath.Join(os.Getenv("HOME"), ".config", "bb", "config.json")
	fixtureFile(t, config, `{"username":"synthetic-user","apiToken":"synthetic-A","authMethod":"basic"}`, 0600)
	a := statusResolveAuthContext("bitbucket.org")
	if !a.known {
		t.Fatal("normal bb basic CLI store unavailable")
	}
	fixtureFile(t, config, `{"username":"synthetic-user","apiToken":"synthetic-B","authMethod":"basic"}`, 0600)
	if a == statusResolveAuthContext("bitbucket.org") {
		t.Fatal("bb CLI credential rotation not invalidated")
	}
	fixtureFile(t, config, `{"authMethod":"oauth","oauthAccessToken":"synthetic-access","oauthRefreshToken":"synthetic-refresh","oauthExpiresAt":1}`, 0600)
	if statusResolveAuthContext("bitbucket.org").known {
		t.Fatal("refreshable OAuth identity falsely established")
	}
}

func TestStatusAuth441UnknownHostSwitchStillInvalidates(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	t.Setenv("GH_HOST", "forge-a.example.invalid")
	ctx := statusAuthOperation(context.Background())
	if statusAuthForContext(ctx, "github.com").known {
		t.Fatal("unknown host routing established identity")
	}
	t.Setenv("GH_HOST", "forge-b.example.invalid")
	if statusAuthSelectionMatches(ctx, "github.com") {
		t.Fatal("unknown host switch did not invalidate in-progress observations")
	}
}

func TestStatusAuth441UnknownNeverRetainsPriorObservation(t *testing.T) {
	url := "https://github.com/example/widget/pull/7"
	old := StatusSnapshot{Collection: "fixture", authContext: "unknown-unavailable", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "MERGED", Freshness: "live", Title: "Prior unknown context"}}}
	incoming := old
	incoming.PRs = []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "UNKNOWN", Freshness: "unavailable"}}
	if got := StatusPreferNewerObservations(incoming, old); got.PRs[0].State != "UNKNOWN" || got.PRs[0].Title != "" {
		t.Fatal("unknown stamp equality granted retained provider authority")
	}
}

func TestStatusAuth441CommittedOrderingReceiptStaysWithPinnedContext(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("GH_TOKEN", "synthetic-A")
	ctx := statusAuthOperation(context.Background())
	started := time.Now()
	// Model the publication boundary: data committed, ordering receipt next.
	if !statusWriteForgeEntryObservedContext(ctx, "probe", "github.com", "example/widget", "7", []byte("newer synthetic observation"), started) {
		t.Fatal("data publication failed")
	}
	t.Setenv("GH_TOKEN", "synthetic-B")
	if !statusWriteForgeEntryObservedContext(ctx, "since", "github.com", "example/widget", "probe:7", []byte(fmt.Sprint(started.UnixNano())), time.Time{}) {
		t.Fatal("context switch interrupted committed ordering receipt")
	}
	if _, ok := statusForgeEntrySince("probe", "github.com", "example/widget", "7"); ok {
		t.Fatal("ordering receipt crossed into B")
	}
	t.Setenv("GH_TOKEN", "synthetic-A")
	if statusWriteForgeEntrySince("probe", "github.com", "example/widget", "7", []byte("older synthetic observation"), started.Add(-time.Second)) {
		t.Fatal("late older writer replaced committed observation after switching back")
	}
	raw, _ := statusReadForgeEntryAny("probe", "github.com", "example/widget", "7")
	if string(raw) != "newer synthetic observation" {
		t.Fatal("ordering lost across credential switch")
	}
}
