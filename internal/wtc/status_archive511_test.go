package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func archive511Entries(updates ...string) string {
	var out []string
	for _, update := range updates {
		state, date, _ := strings.Cut(update, "@")
		out = append(out, `{"pull_request":{"id":4},"update":{"state":"`+state+`","date":"`+date+`"}}`)
	}
	out = append(out, `{"pull_request":{"id":4},"comment":{"id":1}}`, `{"pull_request":{"id":4},"approval":{"date":"2020-09-01T00:00:00Z"}}`)
	return "[" + strings.Join(out, ",") + "]"
}

// archive511Activity is an activity log as bitbucketReadPRActivity returns it.
func archive511Activity(updates ...string) string {
	return `{"activities":` + archive511Entries(updates...) + `}`
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

const archive511Declined = `{"id":4,"state":"DECLINED","title":"x","updated_on":"2020-06-01T00:00:00.000000+00:00","destination":{"branch":{"name":"main"},"repository":{"full_name":"acme/alpha"}},"source":{"commit":{"hash":"aaaaaaaaaaaa"}}}`

// archive511TWG serves PR 4 and its activity through the default twg client,
// in TWG's batch envelope, from files a test can replace. It returns the
// no-bb host, which keeps every other tool and request contained, and the
// directory holding those files.
func archive511TWG(t *testing.T, view, entries string) (noBBHost, string) {
	t.Helper()
	h := newNoBBHost(t)
	for _, name := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	fixtureFile(t, filepath.Join(dir, "view.json"), view, 0644)
	fixtureFile(t, filepath.Join(dir, "activity.json"), entries, 0644)
	fixtureFile(t, filepath.Join(h.bin, "twg"), `#!/bin/sh
if [ "$1" = "--version" ]; then echo 1.4.4; exit 0; fi
printf 'argv:%s\n' "$*" >> "$FAKE_TWG_LOG"
case "$*" in
*"pull-requests get"*) body=view ;;
*"pull-requests activity"*) body=activity; [ -n "$ARCHIVE511_DELAY" ] && sleep "$ARCHIVE511_DELAY" ;;
*) echo '{"ok":false,"error":{"code":"UNKNOWN_COMMAND"}}'; exit 2 ;;
esac
printf '{"items":[{"input":"4","ok":true,"data":%s}]}' "$(cat "`+dir+`/$body.json")"
`, 0755)
	return h, dir
}

func archive511TWGReads(t *testing.T, h noBBHost, kind string) int {
	t.Helper()
	raw, _ := os.ReadFile(h.log)
	return strings.Count(string(raw), "pull-requests "+kind+" ")
}

func TestStatusArchive511TWGDatesDeclineFromActivity(t *testing.T) {
	t.Run("host read credential", func(t *testing.T) {
		archive511TWG(t, archive511Declined, archive511Entries("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z"))
		t.Setenv("TABA_BITBUCKET_READ_CREDENTIAL", "git-credential")
		declined := statusPRDetail{Number: "4", State: "DECLINED", ClosedOn: "2020-06-01T00:00:00.000000+00:00"}
		if got := statusBBCompleteMergeTimeContext(statusAuthOperation(context.Background()), declined, "acme/alpha").ClosedAt; got != "2020-02-03T10:00:00Z" {
			t.Fatalf("host read credential did not date the decline: %q", got)
		}
	})
	h, _ := archive511TWG(t, archive511Declined, archive511Entries("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z"))
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	detail := statusEnrichRecord(PRRecord{Repo: "alpha", Number: "4"}, "acme/alpha", "bitbucket.org")
	if detail.State != "DECLINED" || detail.ClosedAt != "2020-02-03T10:00:00Z" {
		t.Fatalf("decline time not read from activity: %+v", detail)
	}
	if detail.ClosedOn != "2020-06-01T00:00:00.000000+00:00" || detail.MergedOn != "" {
		t.Fatalf("archive eligibility or merge time changed: %+v", detail)
	}
	if gets, reads := archive511TWGReads(t, h, "get"), archive511TWGReads(t, h, "activity"); gets != 1 || reads != 1 {
		t.Fatalf("reads: %d PR, %d activity; want one each", gets, reads)
	}
}

// The decline's activity read budget. A TWG profile has no local identity
// (docs/status-auth-cache.md), so nothing it reads may serve a later run: its
// declines stay unknown and cost no activity read. A known credential keys
// the log by updated_on and reuses it past cache expiry until a reopen or
// re-decline changes updated_on. Without updated_on, or with an incomplete
// log, the time stays unknown.
func TestStatusArchive511TWGDeclineReadBudget(t *testing.T) {
	h, dir := archive511TWG(t, archive511Declined, archive511Entries("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z"))
	t.Setenv("WTC_FORGE_CACHE_AGE", "1")
	declined := statusPRDetail{Number: "4", State: "DECLINED", ClosedOn: "2020-06-01T00:00:00.000000+00:00"}
	complete := func(detail statusPRDetail) string {
		return statusBBCompleteMergeTimeContext(statusAuthOperation(context.Background()), detail, "acme/alpha").ClosedAt
	}
	for range 3 {
		if got := complete(declined); got != "" {
			t.Fatalf("TWG profile dated a decline: %q", got)
		}
	}
	if reads := archive511TWGReads(t, h, "activity"); reads != 0 {
		t.Fatalf("TWG profile: %d activity reads; want none", reads)
	}

	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	if got := complete(declined); got != "2020-02-03T10:00:00Z" {
		t.Fatalf("known credential: %q", got)
	}
	time.Sleep(1100 * time.Millisecond) // past WTC_FORGE_CACHE_AGE
	for range 3 {
		if got := complete(declined); got != "2020-02-03T10:00:00Z" {
			t.Fatalf("expired cache entry for an unchanged decline: %q", got)
		}
	}
	if reads := archive511TWGReads(t, h, "activity"); reads != 1 {
		t.Fatalf("known credential: %d activity reads across expiry; want 1", reads)
	}
	undated := declined
	undated.ClosedOn = ""
	if got := complete(undated); got != "" || archive511TWGReads(t, h, "activity") != 1 {
		t.Fatalf("a decline without updated_on has no invalidation key: %q", got)
	}
	// Reopened and declined again: updated_on moves, so the earlier log is not reused.
	fixtureFile(t, filepath.Join(dir, "activity.json"),
		archive511Entries("OPEN@2020-01-01T00:00:00Z", "DECLINED@2020-02-03T10:00:00Z", "OPEN@2020-03-01T00:00:00Z", "DECLINED@2020-04-01T00:00:00Z"), 0644)
	again := declined
	again.ClosedOn = "2020-04-01T00:00:00.000000+00:00"
	if got := complete(again); got != "2020-04-01T00:00:00Z" {
		t.Fatalf("re-decline dated by an earlier log: %q", got)
	}
	if reads := archive511TWGReads(t, h, "activity"); reads != 2 {
		t.Fatalf("re-decline: %d activity reads; want 2", reads)
	}
	// Another credential is another context: nothing carries over.
	t.Setenv("TWG_BBC_TOKEN", "rotated-twg-token")
	if got := complete(again); got != "2020-04-01T00:00:00Z" || archive511TWGReads(t, h, "activity") != 3 {
		t.Fatalf("rotated credential reused the previous context: %q after %d reads", got, archive511TWGReads(t, h, "activity"))
	}
	// Undatable, incomplete and malformed replies leave the decline unknown.
	// An undatable reply is a complete read, cached like any other; an
	// incomplete (a full page may hide the decline) or malformed one is not
	// cached, so each later operation retries it once, through the same
	// shared admission.
	full := make([]string, 200)
	for i := range full {
		full[i] = "DECLINED@2020-04-01T00:00:00Z"
	}
	for i, tc := range []struct {
		name, entries string
		reads         int
	}{
		{"undatable", archive511Entries("DECLINED@not-a-time"), 1},
		{"incomplete", archive511Entries(full...), 3},
		{"malformed", `{"values":"not a list"}`, 3},
	} {
		fixtureFile(t, filepath.Join(dir, "activity.json"), tc.entries, 0644)
		detail := declined
		detail.ClosedOn = fmt.Sprintf("2020-07-0%dT00:00:00.000000+00:00", i+1)
		before := archive511TWGReads(t, h, "activity")
		for range 3 {
			if got := complete(detail); got != "" {
				t.Fatalf("%s activity dated a decline: %q", tc.name, got)
			}
		}
		if reads := archive511TWGReads(t, h, "activity") - before; reads != tc.reads {
			t.Fatalf("%s: %d activity reads over 3 operations; want %d", tc.name, reads, tc.reads)
		}
	}
	// A retry is an ordinary read: a provider cooldown refuses it.
	statusForgeCool("bitbucket.org", time.Minute)
	before := archive511TWGReads(t, h, "activity")
	cooling := declined
	cooling.ClosedOn = "2020-08-01T00:00:00.000000+00:00"
	if got := complete(cooling); got != "" || archive511TWGReads(t, h, "activity") != before {
		t.Fatalf("a cooling provider was read: %q", got)
	}
}

// Several paths in one operation can complete the same decline: with a
// fresh detail cache, a PR omitted from the open list is completed from the
// cache and again by its full confirmation. An uncached (malformed) reply is
// read once per operation all the same; a new operation retries it.
func TestStatusArchive511DeclineReadOncePerOperation(t *testing.T) {
	h, _ := archive511TWG(t, archive511Declined, `{"values":"not a list"}`)
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	t.Setenv("BB_USERNAME", "synthetic-status-user")
	t.Setenv("BB_API_TOKEN", "synthetic-status-fixture")
	lists := 0
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/repositories/acme/alpha/pullrequests") {
			t.Errorf("unexpected REST request %s", r.URL.Path)
		}
		lists++
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	t.Cleanup(rest.Close)
	previous := statusBBAPI
	statusBBAPI = rest.URL
	t.Cleanup(func() { statusBBAPI = previous })
	record := PRRecord{Repo: "alpha", Number: "4"}
	for operation := 1; operation <= 3; operation++ {
		ctx := statusAuthOperation(context.Background())
		before := archive511TWGReads(t, h, "activity")
		detail := statusEnrichRecordContext(ctx, record, "acme/alpha", "bitbucket.org")
		statusBBCompleteMergeTimeContext(ctx, detail, "acme/alpha") // the collector's pass
		if detail.State != "DECLINED" || detail.ClosedAt != "" {
			t.Fatalf("operation %d: %+v", operation, detail)
		}
		if reads := archive511TWGReads(t, h, "activity") - before; reads != 1 {
			t.Fatalf("operation %d: %d activity reads; want 1", operation, reads)
		}
	}
	if lists == 0 || archive511TWGReads(t, h, "get") != 1 {
		t.Fatalf("the list-omitted cached path was not exercised: %d lists, %d PR reads", lists, archive511TWGReads(t, h, "get"))
	}
}

// statusOperationShared is the operation-owned single flight behind the
// once-per-operation decline read: whatever the interleaving, one call per
// key and operation runs, and every caller in that operation gets its
// outcome, error included.
func TestStatusArchive511OperationSharedCall(t *testing.T) {
	ctx := statusAuthOperation(context.Background())
	failure := errors.New("incomplete")
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func() ([]byte, error) {
		calls.Add(1)
		<-release
		return nil, failure
	}
	const callers = 16
	var ready, finished sync.WaitGroup
	ready.Add(callers)
	finished.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer finished.Done()
			ready.Done()
			_, err := statusOperationShared(ctx, "k", fetch)
			errs <- err
		}()
	}
	ready.Wait()
	time.Sleep(20 * time.Millisecond) // let the callers reach the shared call
	close(release)
	finished.Wait()
	close(errs)
	for err := range errs {
		if err != failure {
			t.Fatalf("a caller got %v, not the shared outcome", err)
		}
	}
	if _, err := statusOperationShared(ctx, "k", fetch); err != failure || calls.Load() != 1 {
		t.Fatalf("%d calls in one operation (later caller got %v); want 1", calls.Load(), err)
	}
	if _, err := statusOperationShared(statusAuthOperation(context.Background()), "k", fetch); err != failure || calls.Load() != 2 {
		t.Fatalf("a new operation must call again: %d calls", calls.Load())
	}
	if _, err := statusOperationShared(ctx, "other", fetch); err != failure || calls.Load() != 3 {
		t.Fatalf("another key must call again: %d calls", calls.Load())
	}
	if _, err := statusOperationShared(context.Background(), "k", fetch); err != failure || calls.Load() != 4 {
		t.Fatalf("without an operation each call runs: %d calls", calls.Load())
	}
}

// Concurrent paths of one operation that complete the same decline while its
// (slow, malformed) activity read is in flight make one read between them.
func TestStatusArchive511ConcurrentDeclinePathsReadOnce(t *testing.T) {
	h, _ := archive511TWG(t, archive511Declined, `{"values":"not a list"}`)
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	t.Setenv("ARCHIVE511_DELAY", "0.3")
	declined := statusPRDetail{Number: "4", State: "DECLINED", ClosedOn: "2020-06-01T00:00:00.000000+00:00"}
	for operation := 1; operation <= 2; operation++ {
		ctx := statusAuthOperation(context.Background())
		var paths sync.WaitGroup
		for range 8 {
			paths.Add(1)
			go func() {
				defer paths.Done()
				if got := statusBBCompleteMergeTimeContext(ctx, declined, "acme/alpha").ClosedAt; got != "" {
					t.Errorf("malformed activity dated a decline: %q", got)
				}
			}()
		}
		paths.Wait()
		if reads := archive511TWGReads(t, h, "activity"); reads != operation {
			t.Fatalf("after %d operations of 8 concurrent paths: %d activity reads; want one per operation", operation, reads)
		}
	}
}

// archive511FakeBB serves one Bitbucket PR and its activity log through the
// deprecated bb client and records every bb call.
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

// bb pr activity stops at its default page limit without saying so, so under
// bb a decline stays unknown and costs no read; merges keep their behaviour.
func TestStatusArchive511BBKeepsDeclineUnknown(t *testing.T) {
	for _, tc := range []struct{ name, view, mergedOn string }{
		{"declined", `{"id":4,"state":"DECLINED","title":"x","updated_on":"2020-06-01T00:00:00Z"}`, ""},
		{"open", `{"id":4,"state":"OPEN","title":"x","source":{"commit":{"hash":"abc"}}}`, ""},
		{"merged", `{"id":4,"state":"MERGED","title":"x","updated_on":"2020-06-01T00:00:00Z"}`, "2020-02-03T10:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := archive511FakeBB(t, tc.view, archive511Activity("OPEN@2020-01-01T00:00:00Z", "MERGED@2020-02-03T10:00:00Z", "DECLINED@2020-02-03T10:00:00Z"))
			// A known bb context: the page limit, not identity, keeps it unknown.
			t.Setenv("BB_USERNAME", "synthetic-status-user")
			t.Setenv("BB_API_TOKEN", "synthetic-status-fixture")
			if !statusEntryKnown(statusAuthOperation(context.Background()), "activity", "bitbucket.org", "example/widget", "4@x") {
				t.Fatal("fixture must give bb a known context")
			}
			detail := statusEnrichRecord(PRRecord{Repo: "widget", Number: "4"}, "example/widget", "bitbucket.org")
			if detail.MergedOn != tc.mergedOn || detail.ClosedAt != "" {
				t.Fatalf("times: %+v", detail)
			}
			if reads := archive511Reads(t, calls, "activity"); (tc.mergedOn == "") != (reads == 0) {
				t.Fatalf("%d activity reads for a %s PR", reads, tc.name)
			}
		})
	}
}
