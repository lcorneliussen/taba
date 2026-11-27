package wtc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func foreground424Fixture(t *testing.T, script string) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("STATUS_424_CALLS", calls)
	for _, name := range []string{"gh", "bb"} {
		fixtureFile(t, filepath.Join(bin, name), "#!/bin/sh\nprintf 'request\\n' >> \"$STATUS_424_CALLS\"\n"+script, 0700)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A local Bitbucket API that lists no open PRs and reports no checks, so
	// no Bitbucket read leaves the machine.
	t.Setenv("BB_API_USER", "synthetic")
	t.Setenv("BB_API_PASS", "synthetic")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"values":[]}`) }))
	old := statusBBAPI
	statusBBAPI = server.URL
	t.Cleanup(func() { statusBBAPI = old; server.Close() })
	return calls
}
func foreground424Budget(t *testing.T, forge string, tokens int, at time.Time) {
	t.Helper()
	statusWriteForgeEntry("budget", forge, "", "", []byte(fmt.Sprintf("%d %d\n", tokens, at.UnixNano())))
}
func foreground424Count(path string) int {
	raw, _ := os.ReadFile(path)
	return strings.Count(string(raw), "request")
}

// foreground424Refresh is an explicit refresh of one PR row: its open list
// is read again in the foreground.
func foreground424Refresh(ctx context.Context, record PRRecord, slug, forge string) (statusPRDetail, StatusObservation) {
	detail := statusEnrichRecordMetadataContext(ctx, StatusRefreshForeground, record, slug, forge)
	return detail, statusDetailObservation(detail)
}

func TestStatusForeground424CooldownAndCustomBudgetAreHonest(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, reason := range []string{"provider-cooldown", "shared-budget"} {
			t.Run(forge+"/"+reason, func(t *testing.T) {
				calls := foreground424Fixture(t, "exit 99\n")
				statusWriteForgeCache(forge, "example/widget", "7", []byte(`{"number":7,"id":7,"state":"OPEN"}`))
				if reason == "provider-cooldown" {
					statusForgeCool(forge, time.Minute)
				} else {
					t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
					foreground424Budget(t, forge, 0, time.Now())
				}
				got, out := foreground424Refresh(context.Background(), PRRecord{Number: "7"}, "example/widget", forge)
				retry, err := time.Parse(time.RFC3339Nano, out.RetryAt)
				if got.State != "OPEN" || got.Freshness != "deferred" || out.Reason != reason || err != nil || !retry.After(time.Now()) || foreground424Count(calls) != 0 {
					t.Fatalf("%+v %+v retryError=%v", got, out, err)
				}
			})
		}
	}
}

func TestStatusForeground424ProviderRetryAfter(t *testing.T) {
	calls := foreground424Fixture(t, "echo 'HTTP 429 Retry-After: 30' >&2\nexit 1\n")
	before := time.Now()
	_, out := foreground424Refresh(context.Background(), PRRecord{Number: "7"}, "example/widget", "github.com")
	retry, err := time.Parse(time.RFC3339Nano, out.RetryAt)
	if err != nil || retry.Before(before.Add(30*time.Second)) || retry.After(time.Now().Add(31*time.Second)) || out.Reason != "provider-cooldown" || foreground424Count(calls) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	_, out = foreground424Refresh(context.Background(), PRRecord{Number: "7"}, "example/widget", "github.com")
	if out.Reason != "provider-cooldown" || foreground424Count(calls) != 1 {
		t.Fatal("cooldown bypassed")
	}
}

func TestStatusForeground424CancellationNeverStartsLater(t *testing.T) {
	for _, blocked := range []string{"list-lock", "budget-lock", "refill"} {
		t.Run(blocked, func(t *testing.T) {
			calls := foreground424Fixture(t, `printf '%s' '{"number":7,"state":"OPEN"}'`)
			forge := "github.com"
			var unlock func()
			if blocked == "list-lock" {
				unlock, _ = statusLockCache(statusForgeEntryPath("openprs", forge, "example/widget", "open"))
			} else if blocked == "budget-lock" {
				unlock, _ = statusLockCache(statusForgeStatePath("budget", forge))
			} else {
				foreground424Budget(t, forge, 0, time.Now())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			_, out := foreground424Refresh(ctx, PRRecord{Number: "7"}, "example/widget", forge)
			if unlock != nil {
				unlock()
			}
			foreground424Budget(t, forge, 16, time.Now())
			if out.Reason != "deadline" || foreground424Count(calls) != 0 {
				t.Fatalf("%s %+v calls=%d", blocked, out, foreground424Count(calls))
			}
		})
	}
	calls := foreground424Fixture(t, "exit 99\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, out := foreground424Refresh(ctx, PRRecord{Number: "7"}, "example/widget", "github.com")
	if out.Reason != "cancelled" || foreground424Count(calls) != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestStatusForeground424ConcurrentRequestsStayWithinBurst(t *testing.T) {
	foreground424Fixture(t, "exit 99\n")
	forge := "github.com"
	foreground424Budget(t, forge, 4, time.Now())
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			results <- statusTakeForegroundToken(ctx, forge)
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		} else if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	}
	if admitted != 4 {
		t.Fatalf("admitted=%d want 4", admitted)
	}
}

func TestStatusForeground424UnsafeLockFailsClosed(t *testing.T) {
	calls := foreground424Fixture(t, "exit 99\n")
	forge := "github.com"
	if !statusSecureCacheDir(statusForgeCacheDir()) {
		t.Fatal("cache fixture")
	}
	target := filepath.Join(t.TempDir(), "target")
	fixtureFile(t, target, "unchanged", 0600)
	if err := os.Symlink(target, statusForgeStatePath("budget", forge)+".lock"); err != nil {
		t.Fatal(err)
	}
	_, out := foreground424Refresh(context.Background(), PRRecord{Number: "7"}, "example/widget", forge)
	raw, _ := os.ReadFile(target)
	// A budget that cannot be accounted admits nothing.
	if out.Reason != "shared-budget" || foreground424Count(calls) != 0 || string(raw) != "unchanged" {
		t.Fatalf("%+v", out)
	}
}

func TestStatusForeground562AdmittedAfterRefill(t *testing.T) {
	foreground424Fixture(t, "exit 99\n")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "1")
	forge := "github.com"
	foreground424Budget(t, forge, 0, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := statusTakeForegroundToken(ctx, forge); err != nil {
		t.Fatalf("foreground refresh not admitted after the refill: %v", err)
	}
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Fatalf("admitted after %s from an empty bucket; want a wait for the refill", waited)
	}
}
