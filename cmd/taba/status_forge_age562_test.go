package main

import (
	"testing"
	"time"

	"taba.sh/taba/internal/wtc"
)

func TestStatusTUI562PerForgeListAgeDecidesStaleness(t *testing.T) {
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	t.Setenv("TABA_FORGE_CACHE_AGE", "")
	now := time.Now()
	observed := now.Add(-120 * time.Second).Format(time.RFC3339Nano)
	for _, tc := range []struct{ url, want string }{
		{"https://bitbucket.org/example/widget/pull-requests/7", "live"},
		{"https://github.com/example/widget/pull/7", "stale"},
	} {
		url := tc.url
		row := wtc.StatusPRRow{Repo: "widget", Number: "7", URL: &url, State: "OPEN", Freshness: "live", ObservedAt: observed}
		if got := statusTUIPresentationRow(row, now).Freshness; got != tc.want {
			t.Errorf("%s observed 120s ago: freshness %q, want %q", tc.url, got, tc.want)
		}
	}
}
