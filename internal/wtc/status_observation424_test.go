package wtc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusObservation424DelayedResponseCannotOverwriteNewerObservation(t *testing.T) {
	for _, winnerState := range []string{"DRAFT", "MERGED"} {
		t.Run(winnerState, func(t *testing.T) {
			calls := foreground424Fixture(t, `touch "$STATUS_424_READY"
while [ ! -f "$STATUS_424_RELEASE" ]; do sleep 0.01; done
printf '%s' '{"number":7,"state":"OPEN","title":"Delayed"}'
`)
			dir := t.TempDir()
			ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
			t.Setenv("STATUS_424_READY", ready)
			t.Setenv("STATUS_424_RELEASE", release)
			forge, slug := "github.com", "example/widget"
			old := time.Now().Add(-time.Hour)
			statusWriteForgeEntrySinceObserved("pr", forge, slug, "7", []byte(`{"number":7,"state":"OPEN","title":"Old"}`), old, old)
			type result struct {
				detail  statusPRDetail
				outcome StatusObservation
			}
			done := make(chan result, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() {
				d, o := statusRefreshRecord(ctx, StatusRefreshForeground, PRRecord{Number: "7"}, slug, forge)
				done <- result{d, o}
			}()
			deadline := time.Now().Add(2 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("synthetic provider did not start")
				}
				time.Sleep(time.Millisecond)
			}
			// Model a released/concurrent publisher; deliberately bypass this reader's
			// ordinary record lock, but retain the existing publication protocol.
			at := time.Now()
			raw := []byte(fmt.Sprintf(`{"number":7,"state":%q,"isDraft":%v,"title":"Newer"}`, winnerState, winnerState == "DRAFT"))
			if !statusWriteForgeEntrySinceObserved("pr", forge, slug, "7", raw, at, at) {
				t.Fatal("newer writer did not publish")
			}
			fixtureFile(t, release, "", 0600)
			got := <-done
			if got.detail.State != winnerState || got.detail.Title != "Newer" || got.detail.ObservedAt != at.UTC().Format(time.RFC3339Nano) || got.outcome.ProviderConfirmed || got.outcome.Reason != "conflicting-observation" || foreground424Count(calls) != 1 {
				t.Fatalf("%+v %+v", got.detail, got.outcome)
			}
			saved, savedAt, ok := statusReadForgeEntryObserved("pr", forge, slug, "7", time.Hour)
			if !ok || !strings.Contains(string(saved), "Newer") || !savedAt.Equal(at) {
				t.Fatalf("winner lost: %s %v", saved, savedAt)
			}
		})
	}
}

func TestStatusObservation424SecondProcessReusesExactTime(t *testing.T) {
	if os.Getenv("STATUS_424_CACHE_CHILD") == "1" {
		got, ok := statusReadObservedDetail(PRRecord{Number: "7"}, "example/widget", "github.com", time.Hour)
		if !ok || got.Title != "Provider title" || got.ObservedAt != os.Getenv("STATUS_424_EXPECT_TIME") {
			t.Fatalf("%+v", got)
		}
		return
	}
	calls := foreground424Fixture(t, `printf '%s' '{"number":7,"state":"OPEN","title":"Provider title"}'`)
	got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, "example/widget", "github.com")
	if !out.ProviderConfirmed {
		t.Fatalf("%+v", out)
	}
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestStatusObservation424SecondProcessReusesExactTime$")
	cmd.Env = append(os.Environ(), "STATUS_424_CACHE_CHILD=1", "STATUS_424_EXPECT_TIME="+got.ObservedAt)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v", raw, err)
	}
	if foreground424Count(calls) != 1 {
		t.Fatal("cache consumer made a provider request")
	}
}

func TestStatusObservation424FailureDoesNotRedateFacts(t *testing.T) {
	for _, script := range []string{"echo 'provider unavailable' >&2\nexit 1\n", "printf '%s' 'invalid JSON'\n", "printf '%s' '{\"number\":7,\"state\":\"UNKNOWN\"}'\n"} {
		t.Run(fmt.Sprint(len(script)), func(t *testing.T) {
			foreground424Fixture(t, script)
			old := time.Now().Add(-time.Hour)
			forge, slug := "github.com", "example/widget"
			statusWriteForgeEntrySinceObserved("pr", forge, slug, "7", []byte(`{"number":7,"state":"OPEN","title":"Saved"}`), old, old)
			got, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, PRRecord{Number: "7"}, slug, forge)
			if got.State != "OPEN" || got.Title != "Saved" || got.Freshness != "unavailable" || got.ObservedAt != old.UTC().Format(time.RFC3339Nano) || out.ObservedAt != got.ObservedAt || out.Reason != "refresh-failed" || out.RetryAt != "" || out.ProviderConfirmed {
				t.Fatalf("%+v %+v", got, out)
			}
			_, at, _ := statusReadForgeEntryObserved("pr", forge, slug, "7", 24*time.Hour)
			if !at.Equal(old) {
				t.Fatal("failure renewed cache age")
			}
		})
	}
}

func TestStatusObservation424ReasonIsNeverInventedFromAge(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		detail statusPRDetail
		reason string
	}{
		{statusPRDetail{State: "OPEN", Freshness: "stale", ObservedAt: old}, "observation-expired"},
		{statusPRDetail{State: "OPEN", Freshness: "deferred", ObservedAt: old}, "unknown"},
		{statusPRDetail{State: "OPEN", Freshness: "unavailable", ObservedAt: old}, "unknown"},
		{statusPRDetail{State: "UNKNOWN", Freshness: "unavailable"}, "never-observed"},
		{statusPRDetail{State: "OPEN", Freshness: "cached", ObservedAt: old}, ""},
	} {
		out := statusDetailObservation(tc.detail)
		if out.Reason != tc.reason || out.RetryAt != "" || out.ProviderConfirmed {
			t.Fatalf("%+v", out)
		}
		raw, _ := json.Marshal(out)
		var copy StatusObservation
		json.Unmarshal(raw, &copy)
		if copy != out {
			t.Fatal("structured reason lost")
		}
	}
}

func TestStatusObservation424CooldownDoesNotShorten(t *testing.T) {
	foreground424Fixture(t, "exit 99\n")
	forge := "github.com"
	statusForgeCool(forge, time.Minute)
	until := statusForgeCoolingUntil(forge)
	statusForgeCool(forge, time.Second)
	if !statusForgeCoolingUntil(forge).Equal(until) {
		t.Fatal("a shorter retry shortened existing cooldown")
	}
}

func TestStatusObservation424InvalidIdentityNeverDispatches(t *testing.T) {
	calls := foreground424Fixture(t, "exit 99\n")
	for _, record := range []PRRecord{{Number: "../7"}, {Number: "0"}, {Number: "7;echo"}} {
		_, out := statusRefreshRecord(context.Background(), StatusRefreshForeground, record, "example/widget", "github.com")
		if out.Reason != "refresh-failed" || foreground424Count(calls) != 0 {
			t.Fatalf("%+v", out)
		}
	}
}
