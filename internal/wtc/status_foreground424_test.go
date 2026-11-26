package wtc

import (
	"context"
	"errors"
	"fmt"
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

func TestStatusForeground424ConfirmsTransitionsDespiteStaleList(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, transition := range []string{"merged", "ready"} {
			t.Run(forge+"/"+transition, func(t *testing.T) {
				state := "MERGED"
				draft := false
				if transition == "ready" {
					state = "OPEN"
					draft = true
				}
				response := fmt.Sprintf(`{"id":7,"number":7,"state":%q,"draft":false,"isDraft":false,"title":"Current title","merged_on":"2026-11-16T10:00:00Z","mergedAt":"2026-11-16T10:00:00Z"}`, state)
				calls := foreground424Fixture(t, "printf '%s' '"+response+"'\n")
				now := time.Now()
				old := now.Add(-time.Hour)
				slug := "example/widget"
				raw := []byte(fmt.Sprintf(`{"id":7,"number":7,"state":"OPEN","draft":%v,"isDraft":%v,"title":"Old title"}`, draft, draft))
				statusWriteForgeEntrySinceObserved("pr", forge, slug, "7", raw, old, old)
				list, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Raw: raw, Fingerprint: "old"}}, false, old)
				statusWriteForgeEntrySinceObserved("openprs", forge, slug, "open", list, old, old)
				statusMarkFullRefresh(forge, slug, "7") // foreground also bypasses a recent audit
				burst, _ := statusForgeAllowance(forge)
				foreground424Budget(t, forge, burst, now)
				got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, slug, forge)
				if got.State != state || !out.ProviderConfirmed || out.Freshness != "live" || foreground424Count(calls) != 1 {
					t.Fatalf("detail=%+v outcome=%+v calls=%d", got, out, foreground424Count(calls))
				}
				saved, at, ok := statusReadForgeEntryObserved("pr", forge, slug, "7", time.Hour)
				if !ok || !strings.Contains(string(saved), "Current title") || at.Before(now) || at.After(time.Now()) || got.ObservedAt != at.UTC().Format(time.RFC3339Nano) {
					t.Fatalf("provider time not persisted: at=%v detail=%+v", at, got)
				}
				// Shared readers preserve the observation and do not run the provider.
				observed, ok := statusReadObservedDetail(PRRecord{Number: "7"}, slug, forge, time.Hour)
				if !ok || observed.State != state || observed.ObservedAt != got.ObservedAt || foreground424Count(calls) != 1 {
					t.Fatalf("cache reuse: %+v", observed)
				}
				var tokens int
				budget, _ := os.ReadFile(statusForgeStatePath("budget", forge))
				fmt.Sscanf(string(budget), "%d", &tokens)
				if tokens != burst-1 {
					t.Fatalf("foreground spent %d tokens; want one", burst-tokens)
				}
			})
		}
	}
}

func TestStatusForeground424BypassesFreshTTL(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			calls := foreground424Fixture(t, `printf '%s' '{"id":7,"number":7,"state":"OPEN","title":"Observed now"}'`)
			statusWriteForgeCache(forge, "example/widget", "7", []byte(`{"id":7,"number":7,"state":"OPEN","title":"Cached"}`))
			statusMarkFullRefresh(forge, "example/widget", "7")
			got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", forge)
			if got.Title != "Observed now" || !out.ProviderConfirmed || foreground424Count(calls) != 1 {
				t.Fatalf("%+v %+v", got, out)
			}
		})
	}
}

func TestStatusForeground424RecordedAndRetainedMergeDoNotFetchMetadata(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			calls := foreground424Fixture(t, "exit 99\n")
			record := PRRecord{Number: "7", Title: "Finished"}
			at := time.Now().Add(-time.Hour)
			if retained {
				statusWriteForgeEntrySinceObserved("pr", "github.com", "example/widget", "7", []byte(`{"number":7,"state":"MERGED"}`), at, at)
			} else {
				record.MergedOn = "2020-01-01T00:00:00Z"
				record.FinalChecks = "SUCCESS"
			}
			got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, record, "example/widget", "github.com")
			if got.State != "MERGED" || out.ProviderConfirmed || foreground424Count(calls) != 0 {
				t.Fatalf("%+v %+v", got, out)
			}
			if retained && got.ObservedAt != at.UTC().Format(time.RFC3339Nano) {
				t.Fatal("retained merge redated")
			}
		})
	}
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
				got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", forge)
				retry, err := time.Parse(time.RFC3339Nano, out.RetryAt)
				if got.State != "OPEN" || got.Freshness != "deferred" || out.Reason != reason || err != nil || !retry.After(time.Now()) || out.ProviderConfirmed || foreground424Count(calls) != 0 {
					t.Fatalf("%+v %+v retryError=%v", got, out, err)
				}
			})
		}
	}
}

func TestStatusForeground424ProviderRetryAfter(t *testing.T) {
	calls := foreground424Fixture(t, "echo 'HTTP 429 Retry-After: 30' >&2\nexit 1\n")
	before := time.Now()
	_, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "github.com")
	retry, err := time.Parse(time.RFC3339Nano, out.RetryAt)
	if err != nil || retry.Before(before.Add(30*time.Second)) || retry.After(time.Now().Add(31*time.Second)) || out.Reason != "provider-cooldown" || foreground424Count(calls) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	_, out = statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "github.com")
	if out.Reason != "provider-cooldown" || foreground424Count(calls) != 1 {
		t.Fatal("cooldown bypassed")
	}
}

func TestStatusForeground424CancellationNeverStartsLater(t *testing.T) {
	for _, blocked := range []string{"record-lock", "budget-lock", "refill"} {
		t.Run(blocked, func(t *testing.T) {
			calls := foreground424Fixture(t, `printf '%s' '{"number":7,"state":"OPEN"}'`)
			forge := "github.com"
			var unlock func()
			if blocked == "record-lock" {
				unlock, _ = statusLockCache(statusForgeEntryPath("pr", forge, "example/widget", "7"))
			} else if blocked == "budget-lock" {
				unlock, _ = statusLockCache(statusForgeStatePath("budget", forge))
			} else {
				foreground424Budget(t, forge, 0, time.Now())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			_, out := statusRefreshRecord(ctx, StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", forge)
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
	_, out := statusRefreshRecord(ctx, StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "github.com")
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
	_, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", forge)
	raw, _ := os.ReadFile(target)
	if out.Reason != "refresh-failed" || foreground424Count(calls) != 0 || string(raw) != "unchanged" {
		t.Fatalf("%+v", out)
	}
}
