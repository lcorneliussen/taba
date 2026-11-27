package wtc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func collector424Collection(t *testing.T, forge string) *Context {
	t.Helper()
	c := watcherCollection(t)
	fixtureFile(t, filepath.Join(c.Harness, adHocRegistryFile), "repos:\n  - name: widget\n    remote: https://"+forge+"/example/widget.git\n    default_ref: origin/main\n", 0600)
	fixtureFile(t, c.PRFile(), "widget 7 - https://"+forge+"/example/widget/"+map[string]string{"github.com": "pull", "bitbucket.org": "pull-requests"}[forge]+"/7 Saved title\n", 0600)
	var err error
	c, err = OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStatusCollector424CooldownAndCancelledWait(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelWait), func(t *testing.T) {
			calls := foreground424Fixture(t, `printf '%s' '{"number":7,"state":"OPEN"}'`)
			c := collector424Collection(t, "github.com")
			if !cancelWait {
				until, _ := statusForgeCoolContext(context.Background(), "github.com", time.Minute)
				got, err := c.StatusForegroundSnapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if len(got.PRs) != 1 || got.PRs[0].Freshness != "deferred" || got.PRs[0].ObservationReason != "provider-cooldown" || got.PRs[0].RetryAt != until.UTC().Format(time.RFC3339Nano) || foreground424Count(calls) != 0 {
					t.Fatalf("%+v", got)
				}
				return
			}
			foreground424Budget(t, "github.com", 0, time.Now())
			fixtureFile(t, filepath.Join(c.Collection, ".wtc-status.json"), "original snapshot bytes", 0600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := c.StatusForegroundSnapshot(ctx); done <- err }()
			// The empty bucket refills in 2 s; the request is waiting for it.
			time.Sleep(100 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancellation reported success")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not complete")
			}
			if foreground424Count(calls) != 0 {
				t.Fatal("cancelled wait dispatched provider")
			}
			bytes, _ := os.ReadFile(filepath.Join(c.Collection, ".wtc-status.json"))
			if string(bytes) != "original snapshot bytes" {
				t.Fatal("cancelled queue persisted snapshot")
			}
		})
	}
}

func TestStatusCollector424ArchivedRecordDoesNotDispatch(t *testing.T) {
	calls := foreground424Fixture(t, "exit 91\n")
	c := collector424Collection(t, "github.com")
	f, err := os.OpenFile(c.PRFile(), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "# merged-pr widget 7 2020-01-01T00:00:00Z SUCCESS")
	f.Close()
	got, err := c.StatusForegroundSnapshot(context.Background())
	if err != nil || len(got.PRs) != 1 || !got.PRs[0].Archived || foreground424Count(calls) != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestStatusCollector424NewerWriterWins(t *testing.T) {
	calls := foreground424Fixture(t, `touch "$STATUS_424_CALLS.entered"
while [ ! -f "$STATUS_424_CALLS.release" ]; do sleep 0.01; done
printf '%s' '{"number":7,"state":"OPEN","title":"Slow old response"}'`)
	c := collector424Collection(t, "github.com")
	done := make(chan StatusSnapshot, 1)
	errs := make(chan error, 1)
	go func() {
		snapshot, err := c.StatusForegroundSnapshot(context.Background())
		done <- snapshot
		errs <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(calls + ".entered"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(calls + ".entered"); err != nil {
		t.Fatal("provider not entered")
	}
	defer os.WriteFile(calls+".release", nil, 0600)
	observed := time.Now()
	raw := []byte(`{"number":7,"state":"MERGED","title":"New confirmed merge","mergedAt":"2026-11-16T10:00:00Z"}`)
	if !statusWriteForgeEntrySinceObserved("pr", "github.com", "example/widget", "7", raw, observed, observed) {
		t.Fatal("concurrent publication rejected")
	}
	os.WriteFile(calls+".release", nil, 0600)
	select {
	case got := <-done:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if got.PRs[0].State != "MERGED" || got.PRs[0].Title != "New confirmed merge" || got.PRs[0].ObservedAt != observed.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("%+v", got.PRs)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector stuck")
	}
}

func TestStatus424PresentationRetainsNewerAndTerminalAuthority(t *testing.T) {
	url := "https://github.com/example/widget/pull/7"
	for _, terminal := range []bool{false, true} {
		now := time.Now()
		state := "DRAFT"
		oldTime := now
		if terminal {
			state = "MERGED"
			oldTime = now.Add(-time.Hour)
		}
		current := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: state, Title: "Authoritative", Freshness: "live", ObservedAt: oldTime.Format(time.RFC3339Nano)}}}
		incoming := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "OPEN", Title: "Old completion", ObservedAt: now.Add(-time.Second).Format(time.RFC3339Nano)}}, Repos: []StatusRepo{{Repo: "widget"}}}
		got := StatusPreferNewerObservations(incoming, current)
		if got.PRs[0].Title != "Authoritative" || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
			t.Fatalf("%+v", got)
		}
	}
}

func TestStatus424NewFailureKeepsOlderFactsWithoutClaimingLive(t *testing.T) {
	url := "https://github.com/example/widget/pull/7"
	current := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "OPEN", Title: "Known title", Freshness: "live", ObservedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano)}}}
	incoming := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "UNKNOWN", Freshness: "deferred", ObservationReason: "shared-budget", RetryAt: "2026-11-16T10:00:00Z"}}}
	got := StatusPreferNewerObservations(incoming, current).PRs[0]
	if got.State != "OPEN" || got.Title != "Known title" || got.ObservedAt != current.PRs[0].ObservedAt || got.Freshness != "deferred" || got.ObservationReason != "shared-budget" || got.RetryAt == "" {
		t.Fatalf("%+v", got)
	}
}

func TestStatus424IncomingTerminalWinsOverLaterActiveTimestamp(t *testing.T) {
	url := "https://github.com/example/widget/pull/7"
	now := time.Now()
	current := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "OPEN", Freshness: "live", ObservedAt: now.Format(time.RFC3339Nano)}}}
	incoming := StatusSnapshot{Collection: "fixture", PRs: []StatusPRRow{{Repo: "widget", Number: "7", URL: &url, State: "MERGED", Freshness: "recorded", ObservedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}}}
	if got := StatusPreferNewerObservations(incoming, current).PRs[0]; got.State != "MERGED" {
		t.Fatalf("terminal authority lost: %+v", got)
	}
	confirmed := "MERGED"
	for _, freshness := range []string{"stale", "unavailable"} {
		incoming.PRs[0].Freshness, incoming.PRs[0].Merge = freshness, &confirmed
		if got := StatusPreferNewerObservations(incoming, current).PRs[0]; got.State != "MERGED" {
			t.Fatalf("confirmed terminal lost after ancillary failure/expiry: %+v", got)
		}
	}
	incoming.PRs[0].Freshness, incoming.PRs[0].Merge = "unavailable", nil
	if got := StatusPreferNewerObservations(incoming, current).PRs[0]; got.State != "OPEN" || got.Freshness != "unavailable" {
		t.Fatalf("unconfirmed merge gained authority: %+v", got)
	}
}
