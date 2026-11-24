package wtc

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
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

// statusArchiveBefore must be a strict weak order over valid, malformed and
// overflowing PR numbers alike, or sorting depends on inventory order.
func TestStatusArchive511ComparatorIsStrictWeakOrder(t *testing.T) {
	var rows []StatusPRRow
	for _, repo := range []string{"gadget", "widget"} {
		for _, at := range []string{"", "not-a-time", "2020-02-03T10:00:00Z", "2020-03-01T00:00:00Z"} {
			for _, number := range []string{"2", "10", "02", "1a", "", "-1", "18446744073709551615", "18446744073709551616", "99999999999999999999"} {
				for _, slug := range []string{"example/a", "example/b"} {
					row := archive511Row(repo, number, "CLOSED", at)
					row.Slug = slug
					rows = append(rows, row)
				}
			}
		}
	}
	less := statusArchiveBefore
	equiv := func(a, b StatusPRRow) bool { return !less(a, b) && !less(b, a) }
	for _, a := range rows {
		if less(a, a) {
			t.Fatalf("irreflexivity: %s#%s", a.Repo, a.Number)
		}
		for _, b := range rows {
			if less(a, b) && less(b, a) {
				t.Fatalf("asymmetry: %s#%s %s#%s", a.Repo, a.Number, b.Repo, b.Number)
			}
			for _, c := range rows {
				if less(a, b) && less(b, c) && !less(a, c) {
					t.Fatalf("transitivity: %q < %q < %q", a.Number, b.Number, c.Number)
				}
				if equiv(a, b) && equiv(b, c) && !equiv(a, c) {
					t.Fatalf("equivalence transitivity: %q %q %q", a.Number, b.Number, c.Number)
				}
			}
		}
	}
	// Every inventory permutation yields the same archive order.
	want := archive511Order(StatusPRsByPriority(rows)) + archive511Slugs(StatusPRsByPriority(rows))
	random := rand.New(rand.NewSource(511))
	for range 200 {
		shuffled := append([]StatusPRRow(nil), rows...)
		random.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		ordered := StatusPRsByPriority(shuffled)
		if got := archive511Order(ordered) + archive511Slugs(ordered); got != want {
			t.Fatalf("order depends on inventory permutation")
		}
	}
	// Parsed numbers precede unparsed ones within one repository and time.
	got := archive511Order(StatusPRsByPriority([]StatusPRRow{
		archive511Row("widget", "1a", "CLOSED", ""), archive511Row("widget", "10", "CLOSED", ""),
		archive511Row("widget", "99999999999999999999", "CLOSED", ""), archive511Row("widget", "2", "CLOSED", ""),
	}))
	if got != "widget#2 widget#10 widget#1a widget#99999999999999999999" {
		t.Fatalf("parsed before unparsed: %s", got)
	}
}

func archive511Slugs(rows []StatusPRRow) string {
	var out []string
	for _, row := range rows {
		out = append(out, row.Slug)
	}
	return " " + strings.Join(out, " ")
}

func archive511Activity(updates ...string) string {
	var out []string
	for _, update := range updates {
		state, date, _ := strings.Cut(update, "@")
		out = append(out, `{"pull_request":{"id":4},"update":{"state":"`+state+`","date":"`+date+`"}}`)
	}
	out = append(out, `{"pull_request":{"id":4},"comment":{"id":1}}`, `{"pull_request":{"id":4},"approval":{"date":"2020-09-01T00:00:00Z"}}`)
	return `{"activities":[` + strings.Join(out, ",") + `]}`
}

// The decline time is the activity update that entered the current DECLINED
// state, never a later edit, a reopen or a guess from an undated update.
func TestStatusArchive511BitbucketDeclineEventTime(t *testing.T) {
	for name, tc := range map[string]struct{ log, want string }{
		"single decline":           {archive511Activity("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00.123456+00:00"), "2020-02-03T10:00:00Z"},
		"offset normalised to UTC": {archive511Activity("DECLINED@2020-02-03T12:00:00+02:00"), "2020-02-03T10:00:00Z"},
		"later edit while declined": {archive511Activity("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z", "DECLINED@2020-05-01T00:00:00Z"),
			"2020-02-03T10:00:00Z"},
		"reopened and declined again": {archive511Activity("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z", "OPEN@2020-03-01T00:00:00Z", "DECLINED@2020-04-01T00:00:00Z"),
			"2020-04-01T00:00:00Z"},
		"out of order": {archive511Activity("DECLINED@2020-04-01T00:00:00Z", "OPEN@2020-01-01T00:00:00Z", "OPEN@2020-03-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z"),
			"2020-04-01T00:00:00Z"},
		"reopen after decline": {archive511Activity("DECLINED@2020-02-03T10:00:00Z", "OPEN@2020-03-01T00:00:00Z"), ""},
		"invalid date":         {archive511Activity("DECLINED@2020-02-03T10:00:00Z", "OPEN@not-a-time"), ""},
		"missing date":         {archive511Activity("OPEN@2020-01-01T00:00:00Z", "DECLINED@"), ""},
		"no state update":      {archive511Activity(), ""},
		"merged only":          {archive511Activity("MERGED@2020-02-03T10:00:00Z"), ""},
		"malformed":            {`{"activities":[{"update":{"state":7}}]}`, ""},
		"not json":             {`not json`, ""},
	} {
		if got := statusBBDeclineEventTime([]byte(tc.log)); got != tc.want {
			t.Errorf("%s: decline time = %q, want %q", name, got, tc.want)
		}
	}
	if got := statusBBMergeEventTime([]byte(archive511Activity("OPEN@2020-01-01T00:00:00Z", "MERGED@2020-02-03T10:00:00Z", "MERGED@2020-03-01T00:00:00Z"))); got != "2020-02-03T10:00:00Z" {
		t.Fatalf("merge time changed: %q", got)
	}
}

// archive511FakeBB serves one Bitbucket PR and its activity log and records
// every bb call.
func archive511FakeBB(t *testing.T, view, activity string) string {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	for name, body := range map[string]string{"view": view, "activity": activity} {
		fixtureFile(t, filepath.Join(bin, name+".json"), body, 0644)
	}
	fixtureFile(t, filepath.Join(bin, "bb"), `#!/bin/sh
echo "$*" >> "`+calls+`"
case "$*" in
  *activity*) cat "`+bin+`/activity.json" ;;
  *view*) cat "`+bin+`/view.json" ;;
  *) exit 2 ;;
esac
`, 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

func archive511Reads(t *testing.T, calls, kind string) int {
	t.Helper()
	raw, _ := os.ReadFile(calls)
	return strings.Count(string(raw), "pr "+kind+" ")
}

func TestStatusArchive511BitbucketDeclineReadsActivityOnce(t *testing.T) {
	view := `{"id":4,"state":"DECLINED","title":"x","updated_on":"2020-06-01T00:00:00.000000+00:00"}`
	calls := archive511FakeBB(t, view, archive511Activity("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z"))
	record := PRRecord{Repo: "widget", Number: "4"}
	for range 2 {
		detail := statusEnrichRecord(record, "example/widget", "bitbucket.org")
		if detail.State != "DECLINED" || detail.ClosedAt != "2020-02-03T10:00:00Z" {
			t.Fatalf("decline time not read from activity: %+v", detail)
		}
		if detail.ClosedOn != "2020-06-01T00:00:00.000000+00:00" || detail.MergedOn != "" {
			t.Fatalf("archive eligibility or merge time changed: %+v", detail)
		}
	}
	if views, activities := archive511Reads(t, calls, "view"), archive511Reads(t, calls, "activity"); views == 0 || activities > views {
		t.Fatalf("activity reads = %d for %d PR reads; want at most one each", activities, views)
	}
}

func TestStatusArchive511BitbucketOtherStatesKeepTheirTimes(t *testing.T) {
	for _, tc := range []struct{ name, view, activity, mergedOn, closedAt string }{
		{"open costs no read", `{"id":4,"state":"OPEN","title":"x","source":{"commit":{"hash":"abc"}}}`, archive511Activity("DECLINED@2020-02-03T10:00:00Z"), "", ""},
		{"merged keeps its merge time", `{"id":4,"state":"MERGED","title":"x","updated_on":"2020-06-01T00:00:00Z"}`,
			archive511Activity("OPEN@2020-01-01T00:00:00Z", "MERGED@2020-02-03T10:00:00Z"), "2020-02-03T10:00:00Z", ""},
		{"reopen in log stays unknown", `{"id":4,"state":"DECLINED","title":"x","updated_on":"2020-06-01T00:00:00Z"}`,
			archive511Activity("DECLINED@2020-02-03T10:00:00Z", "OPEN@2020-03-01T00:00:00Z"), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := archive511FakeBB(t, tc.view, tc.activity)
			detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: "4"}, "example/widget", "bitbucket.org")
			if detail.MergedOn != tc.mergedOn || detail.ClosedAt != tc.closedAt {
				t.Fatalf("times: %+v", detail)
			}
			if n := archive511Reads(t, calls, "activity"); detail.State == "OPEN" && n != 0 {
				t.Fatalf("an open PR read its activity %d times", n)
			}
		})
	}
}
