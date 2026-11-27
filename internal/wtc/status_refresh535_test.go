package wtc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// refresh535Collection enlists, in this order, terminal PRs whose last
// observation is an old decline (a closure on GitHub), then new drafts that
// have never been observed. Enlistment order is the order a user adds PRs over
// time, so the newest work comes last.
func refresh535Collection(t *testing.T, forge string, terminal, fresh []string) *Context {
	t.Helper()
	c := collector424Collection(t, forge)
	path := map[string]string{"github.com": "pull", "bitbucket.org": "pull-requests"}[forge]
	var lines []string
	for _, number := range append(append([]string(nil), terminal...), fresh...) {
		lines = append(lines, fmt.Sprintf("widget %s - https://%s/example/widget/%s/%s Title %s", number, forge, path, number, number))
	}
	fixtureFile(t, c.PRFile(), strings.Join(lines, "\n")+"\n", 0600)
	observed := time.Now().Add(-21 * 24 * time.Hour)
	for _, number := range terminal {
		raw := fmt.Sprintf(`{"id":%[1]s,"number":%[1]s,"state":"DECLINED","title":"Old decline %[1]s","closed_on":"2026-10-01T10:00:00Z","closedAt":"2026-10-01T10:00:00Z"}`, number)
		if forge == "github.com" {
			raw = fmt.Sprintf(`{"number":%[1]s,"state":"CLOSED","title":"Old close %[1]s","closedAt":"2026-10-01T10:00:00Z"}`, number)
		}
		statusWriteForgeEntrySinceObserved("pr", forge, "example/widget", number, []byte(raw), observed, observed)
	}
	return c
}

// The provider lists no open PRs and answers each PR view with its terminal or
// draft state; the number is the third argument for both gh and bb.
func refresh535Provider(t *testing.T, fresh []string) string {
	t.Helper()
	return foreground424Fixture(t, `if [ "$2" = list ]; then printf '[]'; exit 0; fi
printf '%s\n' "$3" >> "$STATUS_424_CALLS.order"
case "$3" in
`+strings.Join(fresh, "|")+`) printf '{"id":%s,"number":%s,"state":"OPEN","draft":true,"isDraft":true,"title":"New draft"}' "$3" "$3" ;;
*) printf '{"id":%s,"number":%s,"state":"DECLINED","title":"Old decline","closed_on":"2026-10-01T10:00:00Z"}' "$3" "$3" ;;
esac
`)
}

// A background pass that ends before a PR gets a slot keeps that PR's last
// observation, deferred, instead of an unknown row (#535).
func TestStatusRefresh535BackgroundDeadlineKeepsLastObservation(t *testing.T) {
	forge := "bitbucket.org"
	refresh535Provider(t, []string{"90"})
	c := refresh535Collection(t, forge, []string{"11"}, nil)
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	details, err := c.statusEnrichRecordsWithIntent(ctx, StatusRefreshBackground, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].State != "DECLINED" || details[0].ObservedAt == "" || details[0].Freshness != "deferred" || statusDetailObservation(details[0]).Reason != "cancelled" {
		t.Fatalf("details=%+v", details)
	}
}
