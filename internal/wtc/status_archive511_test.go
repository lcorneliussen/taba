package wtc

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func archive511Row(repo, number, state, at string) StatusPRRow {
	row := StatusPRRow{Repo: repo, Number: number, State: state, Title: repo + " " + number, Archived: true, Freshness: "live"}
	if at != "" {
		if state == "MERGED" {
			row.MergedOn = &at
		} else {
			row.ClosedAt = &at
		}
	}
	return row
}

func archive511Order(rows []StatusPRRow) string {
	var out []string
	for _, row := range rows {
		out = append(out, row.Repo+"#"+row.Number)
	}
	return strings.Join(out, " ")
}

func TestStatusArchive511DetailKeepsOnlyProviderClosureEvents(t *testing.T) {
	closed, err := statusGHDetail([]byte(`{"number":4,"state":"CLOSED","title":"x","closedAt":"2020-02-01T00:00:00Z"}`), PRRecord{})
	if err != nil || closed.ClosedAt != "2020-02-01T00:00:00Z" {
		t.Fatalf("GitHub closedAt lost: %+v %v", closed, err)
	}
	merged, err := statusGHDetail([]byte(`{"number":4,"state":"CLOSED","title":"x","mergedAt":"2020-02-01T00:00:00Z","closedAt":"2020-02-01T00:00:00Z"}`), PRRecord{})
	if err != nil || merged.State != "MERGED" || merged.ClosedAt != "" || merged.MergedOn == "" {
		t.Fatalf("merged closure must date by merge: %+v %v", merged, err)
	}
	declined, err := statusBBDetail([]byte(`{"id":4,"state":"DECLINED","title":"x","updated_on":"2020-02-01T00:00:00.000000+00:00"}`), PRRecord{})
	if err != nil || declined.ClosedAt != "" {
		t.Fatalf("Bitbucket updated_on is not a closure event: %+v %v", declined, err)
	}
}

func TestStatusArchive511TerminalEventOrder(t *testing.T) {
	recent := "2026-11-20T00:00:00Z"
	rows := []StatusPRRow{
		{Repo: "widget", Number: "40", State: "OPEN", Title: "active"},
		archive511Row("widget", "90", "CLOSED", "2020-01-06T10:00:00Z"),
		archive511Row("tool", "4", "DECLINED", ""),
		archive511Row("gadget", "20", "CLOSED", "2020-02-03T10:00:00Z"),
		archive511Row("widget", "3", "MERGED", "2020-03-10T10:00:00Z"),
		archive511Row("gadget", "5", "MERGED", "2020-02-03T10:00:00Z"),
		archive511Row("tool", "2", "DECLINED", "not-a-time"),
		{Repo: "widget", Number: "41", State: "DRAFT", Draft: true, Title: "draft"},
	}
	// A recently observed old closure keeps its closure time.
	rows[1].ObservedAt = recent
	got := archive511Order(StatusPRsByPriority(rows))
	want := "widget#40 widget#41 widget#3 gadget#5 gadget#20 widget#90 tool#2 tool#4"
	if got != want {
		t.Fatalf("order\n got: %s\nwant: %s", got, want)
	}
	// Inventory order must not leak into equal or unknown rows.
	reversed := append([]StatusPRRow(nil), rows...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	got = archive511Order(StatusPRsByPriority(reversed))
	if !strings.HasSuffix(got, "widget#3 gadget#5 gadget#20 widget#90 tool#2 tool#4") {
		t.Fatalf("archive order depends on inventory order: %s", got)
	}
}

func TestStatusArchive511ReopenAndReclosure(t *testing.T) {
	older := archive511Row("widget", "7", "MERGED", "2020-03-01T00:00:00Z")
	reclosed := archive511Row("widget", "2", "CLOSED", "2020-01-01T00:00:00Z")
	if got := archive511Order(StatusPRsByPriority([]StatusPRRow{older, reclosed})); got != "widget#7 widget#2" {
		t.Fatalf("first closure: %s", got)
	}
	reopened := reclosed
	reopened.State, reopened.Archived, reopened.ClosedAt = "OPEN", false, nil
	if got := archive511Order(StatusPRsByPriority([]StatusPRRow{older, reopened})); got != "widget#2 widget#7" {
		t.Fatalf("a reopened PR leaves the archive: %s", got)
	}
	again := "2020-04-01T00:00:00Z"
	reclosed.ClosedAt = &again
	if got := archive511Order(StatusPRsByPriority([]StatusPRRow{older, reclosed})); got != "widget#2 widget#7" {
		t.Fatalf("reclosure orders by the new closure: %s", got)
	}
}

func TestStatusArchive511MarkdownAndSnapshotRoundTrip(t *testing.T) {
	s := StatusSnapshot{PRs: []StatusPRRow{
		archive511Row("widget", "9", "CLOSED", "2020-01-06T10:00:00Z"),
		archive511Row("widget", "3", "MERGED", "2020-03-10T10:00:00Z"),
		archive511Row("gadget", "12", "CLOSED", "2020-03-02T10:00:00Z"),
	}}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back StatusSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.PRs[0].ClosedAt == nil || *back.PRs[0].ClosedAt != "2020-01-06T10:00:00Z" {
		t.Fatalf("closed_at lost in snapshot: %s", raw)
	}
	md := back.Markdown()
	archived := md[strings.Index(md, "## Archived"):]
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^- \*\*(\w+)\*\* #(\d+)`).FindAllStringSubmatch(archived, -1) {
		got = append(got, m[1]+"#"+m[2])
	}
	if strings.Join(got, " ") != "widget#3 gadget#12 widget#9" {
		t.Fatalf("markdown archive order: %v", got)
	}
}
