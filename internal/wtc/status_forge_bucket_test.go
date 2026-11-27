package wtc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Concurrent panes share one bucket per forge on a fake clock. Provider reads
// never exceed burst + elapsed/every, a 429 refuses every caller until its
// Retry-After, and outside that cooldown no token stays unspent while a caller
// is refused. Queue files that released clients still write hold nothing back.
func TestStatusForgeBucketCeilingCooldownAndNoIdleTokens(t *testing.T) {
	const forge, callers, step, steps, limitFrom = "bitbucket.org", 6, 2 * time.Second, 60, 20
	const retryAfter = 30 * time.Second
	foreground424Fixture(t, "exit 0\n")
	burst, every := statusForgeAllowance(forge)
	start := time.Now()
	now := start
	statusForgeNow = func() time.Time { return now }
	t.Cleanup(func() { statusForgeNow = time.Now })

	if !statusSecureCacheDir(statusForgeCacheDir()) {
		t.Fatal("cache directory")
	}
	key, seen := strings.Repeat("a", 64), start.UTC().Format(time.RFC3339Nano)
	legacy := map[string]string{
		statusForgeStatePath("pending", forge):              fmt.Sprintf(`[{"key":%q,"seen":%q}]`, key, seen),
		statusForgeStatePath("budget", forge) + ".ordinary": fmt.Sprintf(`{"queue":[{"key":%q,"since":%q,"seen":%q}]}`, key, seen, seen),
	}
	for path, raw := range legacy {
		fixtureFile(t, path, raw, 0600)
	}

	var reads atomic.Int32
	var limited atomic.Bool
	var coolUntil time.Time
	for i := range steps {
		now = start.Add(time.Duration(i) * step)
		before := reads.Load()
		refusals := make(chan statusRefusal, callers)
		var wg sync.WaitGroup
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, live, refused, err := statusWithForgeAdmissionContext(context.Background(), StatusRefreshBackground, forge, nil, func() ([]byte, error) {
					reads.Add(1)
					if i >= limitFrom && limited.CompareAndSwap(false, true) {
						return nil, fmt.Errorf("HTTP 429 Retry-After: %d", int(retryAfter.Seconds()))
					}
					return []byte("ok"), nil
				})
				if !live && !statusRateLimited(err) {
					refusals <- refused
				}
			}()
		}
		wg.Wait()
		close(refusals)
		// In the step that met the 429, callers refused before and after it
		// was recorded give different reasons.
		limitStep := coolUntil.IsZero() && limited.Load()
		if limitStep {
			coolUntil = now.Add(retryAfter)
		}

		total, elapsed := int(reads.Load()), now.Sub(start)
		ceiling := burst + int(elapsed/every)
		if total > ceiling {
			t.Fatalf("at %s: %d provider reads, ceiling %d", elapsed, total, ceiling)
		}
		refused := 0
		for refusal := range refusals {
			refused++
			cooling := now.Before(coolUntil)
			switch {
			case limitStep:
			case cooling && (refusal.reason != "provider-cooldown" || !refusal.retry.Equal(coolUntil)):
				t.Fatalf("at %s during the cooldown: %+v, want provider-cooldown until %s", elapsed, refusal, coolUntil.Sub(start))
			case !cooling && (refusal.reason != "shared-budget" || !refusal.retry.After(now)):
				t.Fatalf("at %s: %+v, want shared-budget with a later retry", elapsed, refusal)
			}
		}
		if now.Before(coolUntil) && !limitStep {
			if reads.Load() != before {
				t.Fatalf("at %s: a provider read during the cooldown", elapsed)
			}
			continue
		}
		if refused > 0 && total != ceiling {
			t.Fatalf("at %s: %d callers refused with %d of %d tokens spent", elapsed, refused, total, ceiling)
		}
	}
	if coolUntil.IsZero() || !now.After(coolUntil) {
		t.Fatal("the fixture never reached the rate limit and its end")
	}
	for path, raw := range legacy {
		if got, err := os.ReadFile(path); err != nil || string(got) != raw {
			t.Fatalf("legacy queue file changed: %q %v", got, err)
		}
	}
	var tokens, refilled int64
	if raw, err := os.ReadFile(statusForgeStatePath("budget", forge)); err != nil {
		t.Fatal(err)
	} else if n, err := fmt.Sscanf(string(raw), "%d %d\n", &tokens, &refilled); n != 2 || err != nil {
		t.Fatalf("budget file %q is not the released two-field format", raw)
	}
}

// A refused read serves prior bytes under their original observation time:
// skipped provider reads never freshen cached facts.
func TestStatusForgeRefusalKeepsPriorObservationTime(t *testing.T) {
	const forge, slug, number = "bitbucket.org", "example/widget", "21"
	foreground424Fixture(t, "exit 0\n")
	foreground424Budget(t, forge, 0, time.Now())
	old := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	saved := []byte(`{"id":21,"state":"OPEN"}`)
	if !statusWriteForgeEntryObserved("pr", forge, slug, number, saved, old) {
		t.Fatal("seed stale entry")
	}
	raw, live, refused, err := statusFetchSharedAdmissionContext(context.Background(), "pr", forge, slug, number, func() ([]byte, error) {
		t.Fatal("fetched without admission")
		return nil, nil
	})
	if err != nil || live || string(raw) != string(saved) || refused.reason != "shared-budget" || refused.retry.IsZero() {
		t.Fatalf("stale fallback: live=%v err=%v %+v", live, err, refused)
	}
	_, observed, ok := statusReadForgeEntryObservedContext(context.Background(), "pr", forge, slug, number, 365*24*time.Hour)
	if !ok || !observed.Equal(old) {
		t.Fatalf("observation time %s, want %s", observed, old)
	}
	if !errors.Is(statusTakeForgeToken(context.Background(), forge), errForgeBudget) {
		t.Fatal("the refused read spent a token")
	}
}
