package wtc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestStatusClosedArchiveCollector(t *testing.T) {
	for _, tc := range []struct{ forge, state string }{{"github.com", "CLOSED"}, {"bitbucket.org", "DECLINED"}} {
		for _, date := range []string{"2020-01-01T00:00:00Z", time.Now().UTC().Format(time.RFC3339), "", "malformed", time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)} {
			t.Run(tc.forge+"/"+date, func(t *testing.T) {
				raw := fmt.Sprintf(`{"number":7,"id":7,"state":%q,"title":"Synthetic closure","closedAt":%q,"updated_on":%q}`, tc.state, date, date)
				foreground424Fixture(t, "printf '%s' '"+raw+"'\n")
				c := collector424Collection(t, tc.forge)
				got, err := c.StatusForegroundSnapshot(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if len(got.PRs) != 1 {
					t.Fatalf("closure omitted: %+v", got)
				}
				row := got.PRs[0]
				archived := date == "2020-01-01T00:00:00Z"
				if row.State != tc.state || row.Archived != archived || row.MergedOn != nil || row.OnBranch || row.Landing != nil {
					t.Fatalf("unmerged closure authority: %+v", row)
				}
				text := got.Markdown()
				if !strings.Contains(text, strings.ToLower(tc.state)) {
					t.Fatal(text)
				}
				if archived && (!strings.Contains(text, "~~[") || !strings.Contains(text, "## Archived")) {
					t.Fatal(text)
				}
			})
		}
	}
}

func TestStatusClosedArchiveMergeDistinction(t *testing.T) {
	for _, state := range []string{"CLOSED", "MERGED"} {
		raw := []byte(fmt.Sprintf(`{"number":7,"state":%q,"closedAt":"2020-01-01T00:00:00Z","mergedAt":"2020-01-01T00:00:00Z"}`, state))
		got, err := statusGHDetail(raw, PRRecord{})
		if err != nil || got.State != "MERGED" || got.Merge != "MERGED" {
			t.Fatalf("closed via merge: %+v %v", got, err)
		}
	}
	got, err := statusBBDetail([]byte(`{"id":7,"state":"DECLINED","updated_on":"2020-01-01T00:00:00Z","merge_commit":{"hash":"synthetic"}}`), PRRecord{})
	if err != nil || got.State != "DECLINED" || got.Merge == "MERGED" || got.MergedOn != "" {
		t.Fatal(got, err)
	}
	friday := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	monday := friday.Add(72 * time.Hour)
	wednesday := friday.Add(120 * time.Hour)
	if statusArchived(friday.Format(time.RFC3339), monday) || !statusArchived(friday.Format(time.RFC3339), wednesday) {
		t.Fatal("weekday archive timing changed")
	}
}
