package wtc

import (
	"context"
	"fmt"
	"os"
	"sort"
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

func refresh535Order(calls string) []string {
	raw, _ := os.ReadFile(calls + ".order")
	return strings.Fields(string(raw))
}

// #535: an explicit refresh whose budget admits only a few reads must spend
// them on new drafts before rechecking terminal PRs enlisted ahead of them.
func TestStatusRefresh535ActiveWorkBeforeTerminalRechecks(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		t.Run(forge, func(t *testing.T) {
			fresh := []string{"90", "91", "92"}
			calls := refresh535Provider(t, fresh)
			terminal := []string{"11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22"}
			c := refresh535Collection(t, forge, terminal, fresh)
			// As many admitted reads as drafts. A terminal recheck can only be
			// admitted by a refill, which comes after the first wave on either forge.
			foreground424Budget(t, forge, len(fresh), time.Now())
			got, err := c.statusForegroundSnapshot(context.Background(), 4*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			// Reads within a wave run concurrently; the drafts form the first wave.
			order := refresh535Order(calls)
			first := append([]string(nil), order[:min(len(fresh), len(order))]...)
			sort.Strings(first)
			if strings.Join(first, " ") != strings.Join(fresh, " ") {
				for _, row := range got.PRs {
					t.Logf("#%s %s %s %s", row.Number, row.State, row.Freshness, row.ObservationReason)
				}
				t.Fatalf("provider reads %v, want the drafts %v first", order, fresh)
			}
			if len(got.PRs) != len(terminal)+len(fresh) {
				t.Fatalf("rows=%d want %d: %+v", len(got.PRs), len(terminal)+len(fresh), got.PRs)
			}
			for _, row := range got.PRs {
				if strings.HasPrefix(row.Number, "9") {
					if row.State != "DRAFT" || row.Freshness != "live" {
						t.Fatalf("new draft not observed: %+v", row)
					}
					continue
				}
				// A terminal row keeps its last observation; an unread one is deferred.
				if row.State != "DECLINED" && row.State != "CLOSED" || row.ObservedAt == "" {
					t.Fatalf("terminal row lost its last state: %+v", row)
				}
				if row.Freshness != "live" && (row.Freshness != "deferred" || row.ObservationReason != "deadline") {
					t.Fatalf("terminal row: %+v", row)
				}
			}
		})
	}
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
	details, observations, err := c.statusEnrichRecordsWithIntent(ctx, StatusRefreshBackground, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(details) != 1 || details[0].State != "DECLINED" || details[0].ObservedAt == "" || details[0].Freshness != "deferred" || observations[0].Reason != "cancelled" {
		t.Fatalf("details=%+v observations=%+v", details, observations)
	}
}

// A periodic (background) pass orders its reads the same way: with only enough
// admitted reads for the list and the drafts, the drafts are read and the
// refused terminal rechecks keep their last observation, deferred (#535).
func TestStatusRefresh535BackgroundActiveWorkBeforeTerminalRechecks(t *testing.T) {
	forge := "github.com"
	fresh := []string{"90", "91", "92"}
	calls := refresh535Provider(t, fresh)
	terminal := []string{"11", "12", "13", "14", "15", "16", "17", "18", "19", "20", "21", "22"}
	c := refresh535Collection(t, forge, terminal, fresh)
	records, err := c.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	// One read for the open list, one per draft; a background read is refused,
	// not queued, once the budget is spent.
	foreground424Budget(t, forge, 1+len(fresh), time.Now())
	details, observations, err := c.statusEnrichRecordsWithIntent(context.Background(), StatusRefreshBackground, records, nil)
	if err != nil {
		t.Fatal(err)
	}
	order := refresh535Order(calls)
	first := append([]string(nil), order[:min(len(fresh), len(order))]...)
	sort.Strings(first)
	if strings.Join(first, " ") != strings.Join(fresh, " ") {
		t.Fatalf("provider reads %v, want the drafts %v first", order, fresh)
	}
	for i, detail := range details {
		if strings.HasPrefix(detail.Number, "9") {
			if detail.State != "DRAFT" || statusObservationUnavailable(detail.Freshness) {
				t.Fatalf("new draft not observed: %+v %+v", detail, observations[i])
			}
			continue
		}
		if detail.State != "CLOSED" || detail.ObservedAt == "" {
			t.Fatalf("terminal row lost its last state: %+v %+v", detail, observations[i])
		}
	}
}
