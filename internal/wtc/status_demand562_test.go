package wtc

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// demand562 is a synthetic multi-pane Bitbucket host: several collections
// (panes) enlist PRs in a few repositories and refresh in the background on a
// fake clock, against one shared forge cache. Every provider read is counted
// by kind: open lists and commit statuses at a fake REST API, PR detail and
// activity reads through a fake bb client.
type demand562 struct {
	t      *testing.T
	panes  []*Context
	dir    string
	server *httptest.Server
	mu     sync.Mutex
	lists  map[string]int // slug -> open-list page reads
	other  map[string]int // REST path kind -> reads
	open   map[string]map[string]bool
	// failLists makes every open-list read fail; capLists makes every list
	// run past its page limit, so it is read as capped.
	failLists, capLists bool
}

const demand562Head = "1111111111111111111111111111111111111111"

func demand562Detail(number, state string) string {
	extra := ""
	switch state {
	case "MERGED":
		extra = fmt.Sprintf(`,"merge_commit":{"hash":%q}`, strings.Repeat("2", 40))
	}
	return fmt.Sprintf(`{"id":%s,"state":%q,"draft":false,"title":"Synthetic %s","updated_on":"2026-11-20T10:00:00Z","source":{"commit":{"hash":%q},"branch":{"name":"topic-%s"}},"destination":{"commit":{"hash":%q},"branch":{"name":"main"}}%s}`,
		number, state, number, demand562Head, number, demand562Head, extra)
}

func newDemand562(t *testing.T, panes int, prs map[string]map[string]string) *demand562 {
	t.Helper()
	c := newWorkspaceFixture(t)
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	t.Setenv("WTC_FORGE_FULL_REFRESH", "")
	// No allowance: the fixture counts what the panes ask for, not what a
	// budget lets through.
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("BB_USERNAME", "synthetic")
	t.Setenv("BB_API_TOKEN", "synthetic")
	d := &demand562{t: t, dir: t.TempDir(), lists: map[string]int{}, other: map[string]int{}, open: map[string]map[string]bool{}}
	var enlisted strings.Builder
	for slug, byNumber := range prs {
		repo := strings.TrimPrefix(slug, "example/")
		d.open[slug] = map[string]bool{}
		for number, state := range byNumber {
			d.setPR(slug, number, state)
			fmt.Fprintf(&enlisted, "%s %s topic-%s https://bitbucket.org/%s/pull-requests/%s Synthetic %s\n", repo, number, number, slug, number, number)
		}
	}
	d.server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.server.Close)
	old := statusBBAPI
	statusBBAPI = d.server.URL
	t.Cleanup(func() { statusBBAPI = old })
	fixtureFile(t, filepath.Join(d.dir, "bin", "bb"), `#!/bin/sh
number=$3 repo=$7
printf '%s %s %s\n' "$2" "$repo" "$number" >> "$WTC_TEST_BB_DIR/reads"
case "$2" in
  view) cat "$WTC_TEST_BB_DIR/$repo-$number.json" ;;
  activity) cat "$WTC_TEST_BB_DIR/$repo-$number-activity.json" ;;
  *) exit 77 ;;
esac
`, 0700)
	t.Setenv("WTC_TEST_BB_DIR", d.dir)
	t.Setenv("PATH", filepath.Join(d.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
	for i := 0; i < panes; i++ {
		dir := filepath.Join(c.Workspace, fmt.Sprintf("pane-%d", i))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, "--git-dir="+owner, "worktree", "add", "-q", "--detach", filepath.Join(dir, "harness"), "origin/main")
		fixtureFile(t, filepath.Join(dir, ".wtc-prs"), enlisted.String(), 0600)
		pane, err := OpenCollection(dir)
		if err != nil {
			t.Fatal(err)
		}
		d.panes = append(d.panes, pane)
	}
	return d
}

// setPR sets what the provider reports for one PR: its detail, activity log
// and whether the open list carries it.
func (d *demand562) setPR(slug, number, state string) {
	d.t.Helper()
	repo := strings.TrimPrefix(slug, "example/")
	fixtureFile(d.t, filepath.Join(d.dir, repo+"-"+number+".json"), demand562Detail(number, state), 0600)
	activity := `{"activities":[]}`
	if state == "MERGED" || state == "DECLINED" {
		activity = fmt.Sprintf(`{"activities":[{"update":{"state":%q,"date":"2026-11-20T10:00:00Z"}}]}`, state)
	}
	fixtureFile(d.t, filepath.Join(d.dir, repo+"-"+number+"-activity.json"), activity, 0600)
	d.mu.Lock()
	d.open[slug][number] = state == "OPEN"
	d.mu.Unlock()
}

func (d *demand562) serve(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/repositories/")
	switch {
	case strings.HasSuffix(path, "/pullrequests"):
		slug := strings.TrimSuffix(path, "/pullrequests")
		d.lists[slug]++
		if d.failLists {
			http.Error(w, "synthetic outage", http.StatusInternalServerError)
			return
		}
		var values []string
		for number, open := range d.open[slug] {
			if open {
				values = append(values, demand562Detail(number, "OPEN"))
			}
		}
		if d.capLists {
			var page int
			fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
			values = append(values, demand562Detail(fmt.Sprint(900+page), "OPEN"))
			fmt.Fprintf(w, `{"values":[%s],"next":%q}`, strings.Join(values, ","), fmt.Sprintf("%s/repositories/%s/pullrequests?page=%d", d.server.URL, slug, page+1))
			return
		}
		fmt.Fprintf(w, `{"values":[%s]}`, strings.Join(values, ","))
	case strings.Contains(path, "/statuses"):
		d.other["statuses"]++
		fmt.Fprint(w, `{"values":[]}`)
	default:
		d.other[path]++
		fmt.Fprint(w, `{"values":[]}`)
	}
}

// reads counts bb client reads of one kind (view or activity) for repo#number.
func (d *demand562) reads(kind, repo, number string) int {
	d.t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "reads"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		d.t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if line == kind+" "+repo+" "+number {
			count++
		}
	}
	return count
}

func (d *demand562) listReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, n := range d.lists {
		total += n
	}
	return total
}

// advance moves the fake clock: every cache age is measured from file
// modification times, so moving them back by step is the same as step passing.
func (d *demand562) advance(step time.Duration) {
	d.t.Helper()
	err := filepath.WalkDir(statusForgeCacheDir(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		at := info.ModTime().Add(-step)
		return os.Chtimes(path, at, at)
	})
	if err != nil && !os.IsNotExist(err) {
		d.t.Fatal(err)
	}
}

// pass runs one background refresh in every pane, as status panes do.
func (d *demand562) pass() []StatusSnapshot {
	d.t.Helper()
	out := make([]StatusSnapshot, len(d.panes))
	for i, pane := range d.panes {
		snapshot, err := pane.statusForgePreview(false, true)
		if err != nil {
			d.t.Fatalf("pane %d: %v", i, err)
		}
		out[i] = snapshot
	}
	return out
}

func demand562Row(t *testing.T, snapshot StatusSnapshot, repo, number string) StatusPRRow {
	t.Helper()
	for _, row := range snapshot.PRs {
		if row.Repo == repo && row.Number == number {
			return row
		}
	}
	t.Fatalf("no row for %s#%s in %+v", repo, number, snapshot.PRs)
	return StatusPRRow{}
}

// TestStatusDemand562RoutineBitbucketReads replays three panes over two
// Bitbucket repositories for 30 minutes of 90-second background passes. Each
// repository has an open PR, a declined PR and a merged PR.
func TestStatusDemand562RoutineBitbucketReads(t *testing.T) {
	repos := []string{"example/r1", "example/r2"}
	prs := map[string]map[string]string{}
	for _, slug := range repos {
		prs[slug] = map[string]string{"11": "OPEN", "12": "DECLINED", "13": "MERGED"}
	}
	d := newDemand562(t, 3, prs)
	const interval = 90 * time.Second
	const passes = 20
	for i := 0; i < passes; i++ {
		if i > 0 {
			d.advance(interval + time.Second)
		}
		before := d.listReads()
		snapshots := d.pass()
		// N panes over M repositories read each list at most once per interval.
		if got := d.listReads() - before; got > len(repos) {
			t.Fatalf("pass %d: %d list reads for %d repositories", i, got, len(repos))
		}
		for _, snapshot := range snapshots {
			for _, repo := range []string{"r1", "r2"} {
				if row := demand562Row(t, snapshot, repo, "12"); row.State != "DECLINED" {
					t.Fatalf("pass %d: declined row %+v", i, row)
				}
				if row := demand562Row(t, snapshot, repo, "13"); row.State != "MERGED" {
					t.Fatalf("pass %d: merged row %+v", i, row)
				}
			}
		}
	}
	for _, repo := range []string{"r1", "r2"} {
		t.Logf("%s: open views=%d declined views=%d merged views=%d activity(merged)=%d activity(declined)=%d",
			repo, d.reads("view", repo, "11"), d.reads("view", repo, "12"), d.reads("view", repo, "13"),
			d.reads("activity", repo, "13"), d.reads("activity", repo, "12"))
	}
	t.Logf("list reads=%d other=%v", d.listReads(), d.other)
	for _, repo := range []string{"r1", "r2"} {
		// One terminal reconciliation: a PR off the open list is read once to
		// record how it ended, and never again by background refresh.
		if got := d.reads("view", repo, "13"); got != 1 {
			t.Errorf("%s merged PR read %d times over %d passes, want once", repo, got, passes)
		}
		if got := d.reads("view", repo, "12"); got != 1 {
			t.Errorf("%s declined PR read %d times over %d passes, want once", repo, got, passes)
		}
	}
}

// demand562Unknown makes the Bitbucket credential context unknown (an OAuth
// token in the bb config), while the REST credential still reads: every
// observation then stays private to its status operation (#471).
func demand562Unknown(t *testing.T) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(home, ".config", "bb", "config.json"), `{"username":"synthetic","apiToken":"synthetic","oauthAccessToken":"synthetic"}`, 0600)
}

// In a context whose observations stay private, no pane can reuse another's
// read, so each pane reads a terminal PR once for its own registry, and never
// again.
func TestStatusDemand562PrivateContextReadsTerminalPRsOncePerPane(t *testing.T) {
	const panes = 3
	d := newDemand562(t, panes, map[string]map[string]string{"example/r1": {"11": "OPEN", "12": "DECLINED", "13": "MERGED"}})
	demand562Unknown(t)
	for i := 0; i < 10; i++ {
		if i > 0 {
			d.advance(91 * time.Second)
		}
		for _, snapshot := range d.pass() {
			if !strings.HasPrefix(snapshot.authContext, "unknown-") {
				t.Fatalf("fixture context is known: %q", snapshot.authContext)
			}
			if row := demand562Row(t, snapshot, "r1", "12"); row.State != "DECLINED" {
				t.Fatalf("pass %d: declined row %+v", i, row)
			}
		}
	}
	for _, number := range []string{"12", "13"} {
		if got := d.reads("view", "r1", number); got != panes {
			t.Errorf("terminal PR #%s read %d times by %d panes over 10 passes, want once per pane", number, got, panes)
		}
	}
	t.Logf("list reads=%d (private contexts share none)", d.listReads())
}

func TestStatusDemand562ReopenedDeclineIsOpenAgain(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
	d.pass()
	records, err := d.panes[0].ListPRs()
	if err != nil || len(records) != 1 || records[0].DeclinedOn != "2026-11-20T10:00:00Z" || records[0].DeclinedAt != "" {
		t.Fatalf("decline not recorded: %+v %v", records, err)
	}
	// bb reports no reliable activity page, so the decline event is unknown;
	// updated_on still dates archive eligibility.
	d.setPR("example/r1", "12", "OPEN")
	d.advance(91 * time.Second)
	row := demand562Row(t, d.pass()[0], "r1", "12")
	if row.State != "OPEN" {
		t.Fatalf("reopened PR on the open list kept its recorded decline: %+v", row)
	}
	if records, err := d.panes[0].ListPRs(); err != nil || records[0].DeclinedOn != "" {
		t.Fatalf("reopen did not clear the recorded decline: %+v %v", records, err)
	}
	d.setPR("example/r1", "12", "DECLINED")
	d.advance(91 * time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" {
		t.Fatalf("second decline: %+v", row)
	}
	d.advance(91 * time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("second decline not recorded: %+v", row)
	}
	// One read for each decline; the open interval needed none.
	if got := d.reads("view", "r1", "12"); got != 2 {
		t.Fatalf("PR read %d times, want once per decline", got)
	}
}

// A recorded decline needs a complete, current list to be overridden. A
// failing list, or a provider cooldown, keeps it without reading the PR.
func TestStatusDemand562RecordedDeclineSurvivesListFailureAndCooldown(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
	d.pass()
	reads := d.reads("view", "r1", "12")
	d.failLists = true
	d.advance(91 * time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("list failure: %+v", row)
	}
	d.failLists = false
	d.advance(91 * time.Second)
	if until, err := statusForgeCoolContext(context.Background(), "bitbucket.org", 10*time.Minute); err != nil || until.IsZero() {
		t.Fatalf("cooldown not recorded: %v %v", until, err)
	}
	lists := d.listReads()
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("cooldown: %+v", row)
	}
	if d.listReads() != lists {
		t.Fatal("a list was read during the provider cooldown")
	}
	if got := d.reads("view", "r1", "12"); got != reads {
		t.Fatalf("recorded decline read again: %d reads, had %d", got, reads)
	}
}

// Presence on a current list proves a PR open even when the list was capped;
// a list served stale after a refused read may predate the decline and does
// not override it.
func TestStatusDemand562RecordedDeclineListEvidence(t *testing.T) {
	t.Run("capped", func(t *testing.T) {
		d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
		d.pass()
		d.setPR("example/r1", "12", "OPEN")
		d.capLists = true
		d.advance(91 * time.Second)
		if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "OPEN" {
			t.Fatalf("capped current list carrying the PR: %+v", row)
		}
		if records, err := d.panes[0].ListPRs(); err != nil || records[0].DeclinedOn != "" {
			t.Fatalf("record kept: %+v %v", records, err)
		}
	})
	t.Run("stale", func(t *testing.T) {
		d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "OPEN"}})
		d.pass()
		// A decline recorded after the list was read (for example by `r`).
		registry := filepath.Join(d.panes[0].Collection, ".wtc-prs")
		raw, err := os.ReadFile(registry)
		if err != nil {
			t.Fatal(err)
		}
		fixtureFile(t, registry, string(raw)+"# declined-pr r1 12 2026-11-20T10:00:00Z\n", 0600)
		d.advance(91 * time.Second)
		t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
		statusTakeForgeToken("bitbucket.org")
		views := d.reads("view", "r1", "12")
		if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
			t.Fatalf("stale list overrode the recorded decline: %+v", row)
		}
		if d.reads("view", "r1", "12") != views {
			t.Fatal("recorded decline was read")
		}
	})
}
