package wtc

import (
	"context"
	"errors"
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

// demand562 is a synthetic multi-pane host: several collections (panes)
// enlist PRs in a few repositories and refresh in the background on a fake
// clock, against one shared forge cache. Every provider read is counted by
// kind. On Bitbucket, open lists and commit statuses come from a fake REST
// API and PR detail and activity reads from a fake bb client; on GitHub, a
// fake gh serves open lists (with their check rollup) and PR reads.
type demand562 struct {
	t      *testing.T
	forge  string
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
	// status is the Bitbucket commit status every head reports, or none.
	status string
	// stall holds each open-list read of this slug until its client gives up.
	stall string
	// paged puts a filler PR on a first list page and the open PRs on a second.
	paged bool
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

// demand562GHDetail is a GitHub PR as gh reports it; a decline is a close.
// Its checks have passed.
func demand562GHDetail(number, state string) string {
	extra := ""
	switch state {
	case "DECLINED":
		state, extra = "CLOSED", `,"closedAt":"2026-11-20T10:00:00Z"`
	case "MERGED":
		extra = fmt.Sprintf(`,"mergedAt":"2026-11-20T10:00:00Z","closedAt":"2026-11-20T10:00:00Z","mergeCommit":{"oid":%q}`, strings.Repeat("2", 40))
	}
	return fmt.Sprintf(`{"number":%s,"state":%q,"isDraft":false,"title":"Synthetic %s","updatedAt":"2026-11-20T10:00:00Z","headRefName":"topic-%s","headRefOid":%q,"baseRefName":"main","statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]%s}`,
		number, state, number, number, demand562Head, extra)
}

func newDemand562(t *testing.T, panes int, prs map[string]map[string]string) *demand562 {
	t.Helper()
	return newDemand562On(t, "bitbucket.org", panes, prs)
}

func newDemand562On(t *testing.T, forge string, panes int, prs map[string]map[string]string) *demand562 {
	t.Helper()
	c := newWorkspaceFixture(t)
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
	t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	// A GitHub token makes the GitHub credential context known, so panes
	// share its observations as they share the Bitbucket ones.
	t.Setenv("GH_CONFIG_DIR", filepath.Join(t.TempDir(), "gh"))
	t.Setenv("GH_TOKEN", "synthetic")
	t.Setenv("GITHUB_TOKEN", "")
	// No allowance: the fixture counts what the panes ask for, not what a
	// budget lets through.
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("BB_USERNAME", "synthetic")
	t.Setenv("BB_API_TOKEN", "synthetic")
	d := &demand562{t: t, forge: forge, dir: t.TempDir(), lists: map[string]int{}, other: map[string]int{}, open: map[string]map[string]bool{}}
	var enlisted strings.Builder
	for slug, byNumber := range prs {
		d.open[slug] = map[string]bool{}
		for number, state := range byNumber {
			d.setPR(slug, number, state)
			enlisted.WriteString(d.enlistment(slug, number))
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
	// gh pr list --repo SLUG ... | gh pr view NUMBER --repo SLUG ...
	fixtureFile(t, filepath.Join(d.dir, "bin", "gh"), `#!/bin/sh
case "$2" in
  list) repo=${4#example/}; printf 'list %s\n' "$repo" >> "$WTC_TEST_BB_DIR/reads"
    [ -n "$WTC_TEST_LIST_DELAY" ] && sleep "$WTC_TEST_LIST_DELAY"
    # Like gh, a list carries only the fields --json asks for.
    case "$*" in
      *headRefOid*) cat "$WTC_TEST_BB_DIR/gh-$repo-list.json" ;;
      *) sed 's/"headRefOid":"[0-9a-f]*",//g' "$WTC_TEST_BB_DIR/gh-$repo-list.json" ;;
    esac ;;
  view) repo=${5#example/}; printf 'view %s %s\n' "$repo" "$3" >> "$WTC_TEST_BB_DIR/reads"; cat "$WTC_TEST_BB_DIR/gh-$repo-$3.json" ;;
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

// enlistment is the registry line that enlists slug#number.
func (d *demand562) enlistment(slug, number string) string {
	repo := strings.TrimPrefix(slug, "example/")
	url := "https://bitbucket.org/" + slug + "/pull-requests/" + number
	if d.forge == "github.com" {
		url = "https://github.com/" + slug + "/pull/" + number
	}
	return fmt.Sprintf("%s %s topic-%s %s Synthetic %s\n", repo, number, number, url, number)
}

// enlist adds slug#number to every pane's registry.
func (d *demand562) enlist(slug, number string) {
	d.t.Helper()
	for _, pane := range d.panes {
		registry := filepath.Join(pane.Collection, ".wtc-prs")
		raw, err := os.ReadFile(registry)
		if err != nil {
			d.t.Fatal(err)
		}
		fixtureFile(d.t, registry, string(raw)+d.enlistment(slug, number), 0600)
	}
}

// setPR sets what the provider reports for one PR: its detail, activity log
// and whether the open list carries it.
func (d *demand562) setPR(slug, number, state string) {
	d.t.Helper()
	repo := strings.TrimPrefix(slug, "example/")
	fixtureFile(d.t, filepath.Join(d.dir, repo+"-"+number+".json"), demand562Detail(number, state), 0600)
	fixtureFile(d.t, filepath.Join(d.dir, "gh-"+repo+"-"+number+".json"), demand562GHDetail(number, state), 0600)
	activity := `{"activities":[]}`
	if state == "MERGED" || state == "DECLINED" {
		activity = fmt.Sprintf(`{"activities":[{"update":{"state":%q,"date":"2026-11-20T10:00:00Z"}}]}`, state)
	}
	fixtureFile(d.t, filepath.Join(d.dir, repo+"-"+number+"-activity.json"), activity, 0600)
	d.mu.Lock()
	d.open[slug][number] = state == "OPEN"
	var listed []string
	for open, isOpen := range d.open[slug] {
		if isOpen {
			listed = append(listed, demand562GHDetail(open, "OPEN"))
		}
	}
	d.mu.Unlock()
	fixtureFile(d.t, filepath.Join(d.dir, "gh-"+repo+"-list.json"), "["+strings.Join(listed, ",")+"]", 0600)
}

func (d *demand562) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/repositories/")
	d.mu.Lock()
	stall := d.stall != "" && path == d.stall+"/pullrequests"
	d.mu.Unlock()
	if stall {
		<-r.Context().Done()
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
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
		if d.paged && r.URL.Query().Get("page") == "" {
			fmt.Fprintf(w, `{"values":[%s],"next":%q}`, demand562Detail("900", "OPEN"), d.server.URL+"/repositories/"+slug+"/pullrequests?page=1")
			return
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
		if d.status != "" {
			fmt.Fprintf(w, `{"values":[{"state":%q}]}`, d.status)
			return
		}
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

// listReads counts open-list reads on either forge.
func (d *demand562) listReads() int {
	d.mu.Lock()
	total := 0
	for _, n := range d.lists {
		total += n
	}
	d.mu.Unlock()
	raw, _ := os.ReadFile(filepath.Join(d.dir, "reads"))
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "list ") {
			total++
		}
	}
	return total
}

// checkReads counts Bitbucket check pages: commit statuses and pipelines.
func (d *demand562) checkReads() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := d.other["statuses"]
	for path, n := range d.other {
		if strings.HasSuffix(path, "/pipelines/") {
			total += n
		}
	}
	return total
}

// refresh runs one explicit refresh (`r`) in a pane.
func (d *demand562) refresh(pane int) StatusSnapshot {
	d.t.Helper()
	c := d.panes[pane]
	snapshot, err := c.statusForgePreviewWithIntent(c.resourceContext(), StatusRefreshForeground, false, true, 30*time.Second)
	if err != nil {
		d.t.Fatalf("pane %d refresh: %v", pane, err)
	}
	return snapshot
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
// Bitbucket repositories for twenty background passes, each one Bitbucket
// list age (statusOpenListAge) after the last. Each repository has an open
// PR, a declined PR and a merged PR.
func TestStatusDemand562RoutineBitbucketReads(t *testing.T) {
	repos := []string{"example/r1", "example/r2"}
	prs := map[string]map[string]string{}
	for _, slug := range repos {
		prs[slug] = map[string]string{"11": "OPEN", "12": "DECLINED", "13": "MERGED"}
	}
	d := newDemand562(t, 3, prs)
	interval := statusOpenListAge("bitbucket.org")
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
			d.advance(statusOpenListAge("bitbucket.org") + time.Second)
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
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	row := demand562Row(t, d.pass()[0], "r1", "12")
	if row.State != "OPEN" {
		t.Fatalf("reopened PR on the open list kept its recorded decline: %+v", row)
	}
	if records, err := d.panes[0].ListPRs(); err != nil || records[0].DeclinedOn != "" {
		t.Fatalf("reopen did not clear the recorded decline: %+v %v", records, err)
	}
	d.setPR("example/r1", "12", "DECLINED")
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" {
		t.Fatalf("second decline: %+v", row)
	}
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("second decline not recorded: %+v", row)
	}
	// One read for each decline; the open interval needed none.
	if got := d.reads("view", "r1", "12"); got != 2 {
		t.Fatalf("PR read %d times, want once per decline", got)
	}
}

// A recorded decline needs a current list carrying the PR to be overridden. A
// failing list, or a provider cooldown, keeps it without reading the PR.
func TestStatusDemand562RecordedDeclineSurvivesListFailureAndCooldown(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
	d.pass()
	reads := d.reads("view", "r1", "12")
	d.failLists = true
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("list failure: %+v", row)
	}
	d.failLists = false
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
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
		d.advance(statusOpenListAge("bitbucket.org") + time.Second)
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
		d.advance(statusOpenListAge("bitbucket.org") + time.Second)
		t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
		_ = statusTakeForgeToken(context.Background(), "bitbucket.org")
		views := d.reads("view", "r1", "12")
		if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
			t.Fatalf("stale list overrode the recorded decline: %+v", row)
		}
		if d.reads("view", "r1", "12") != views {
			t.Fatal("recorded decline was read")
		}
	})
}

// A PR reopened and merged while no current list saw it open keeps its
// recorded decline in background refresh; an explicit refresh reads it by
// number, records the merge and drops the decline.
func TestStatusDemand562ExplicitRefreshRepairsAMergeAfterReopen(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
	d.pass()
	d.setPR("example/r1", "12", "MERGED")
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "DECLINED" || row.Freshness != "recorded" {
		t.Fatalf("background refresh: %+v", row)
	}
	views := d.reads("view", "r1", "12")
	pane := d.panes[0]
	snapshot, err := pane.statusForgePreviewWithIntent(pane.resourceContext(), StatusRefreshForeground, false, true, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if row := demand562Row(t, snapshot, "r1", "12"); row.State != "MERGED" {
		t.Fatalf("explicit refresh: %+v", row)
	}
	if got := d.reads("view", "r1", "12"); got != views+1 {
		t.Fatalf("explicit refresh made %d reads, want one", got-views)
	}
	records, err := pane.ListPRs()
	if err != nil || records[0].MergedOn == "" {
		t.Fatalf("merge not recorded: %+v %v", records, err)
	}
	// ListPRs hides a decline behind a recorded merge; the row itself is gone.
	raw, err := os.ReadFile(filepath.Join(pane.Collection, ".wtc-prs"))
	if err != nil || strings.Contains(string(raw), "# declined-pr r1 12 ") {
		t.Fatalf("decline row left after the merge:\n%s %v", raw, err)
	}
	d.advance(statusOpenListAge("bitbucket.org") + time.Second)
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "MERGED" || d.reads("view", "r1", "12") != views+1 {
		t.Fatalf("recorded merge: %+v", row)
	}
}

// A recorded decline carries its dates and is archived by them, and a pass
// that gets no turn for it (here an explicit refresh whose phase has already
// ended) still shows it, even where no cached observation exists.
func TestStatusDemand562RecordedDeclineDatesAndDeferredPasses(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"12": "DECLINED"}})
	demand562Unknown(t)
	pane := d.panes[0]
	registry := filepath.Join(pane.Collection, ".wtc-prs")
	raw, err := os.ReadFile(registry)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, registry, string(raw)+"# declined-pr r1 12 2026-11-02T10:00:00Z 2026-11-02T09:59:00Z\n", 0600)
	row := demand562Row(t, d.pass()[0], "r1", "12")
	if row.State != "DECLINED" || row.Freshness != "recorded" || !row.Archived || row.ClosedAt == nil || *row.ClosedAt != "2026-11-02T09:59:00Z" {
		t.Fatalf("recorded decline row: %+v closed_at=%v", row, row.ClosedAt)
	}
	snapshot, err := pane.statusForgePreviewWithIntent(pane.resourceContext(), StatusRefreshForeground, false, true, time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if row := demand562Row(t, snapshot, "r1", "12"); row.State != "DECLINED" || !row.Archived {
		t.Fatalf("deferred pass dropped the recorded decline: %+v", row)
	}
	// A cancelled operation gives the PR no turn.
	records, err := pane.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if detail := statusEnrichRecordMetadataContext(cancelled, StatusRefreshBackground, records[0], "example/r1", "bitbucket.org"); detail.State != "DECLINED" || detail.ClosedOn != "2026-11-02T10:00:00Z" {
		t.Fatalf("cancelled: %+v", detail)
	}
	if got := d.reads("view", "r1", "12"); got != 0 {
		t.Fatalf("recorded decline read %d times", got)
	}
}

// The behaviour tests below pin that the open list is the only routine read.

func demand562Record(t *testing.T, pane *Context, number string) PRRecord {
	t.Helper()
	records, err := pane.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Number == number {
			return record
		}
	}
	t.Fatalf("PR %s not enlisted: %+v", number, records)
	return PRRecord{}
}

// views counts PR reads by number on either forge.
func (d *demand562) views() int {
	d.t.Helper()
	raw, _ := os.ReadFile(filepath.Join(d.dir, "reads"))
	return strings.Count(string(raw), "view ")
}

// Three panes over two repositories, each with an open, a closed (declined)
// and a merged PR whose checks passed, refresh three times within each list
// interval for four intervals. Each list is read at most once per interval,
// a PR off the list is read once and never again, and an open PR is not read
// on its own at all.
func TestStatusDemand562ListIsTheOnlyRoutineRead(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		t.Run(forge, func(t *testing.T) {
			repos := []string{"example/r1", "example/r2"}
			prs := map[string]map[string]string{}
			for _, slug := range repos {
				prs[slug] = map[string]string{"11": "OPEN", "12": "DECLINED", "13": "MERGED"}
			}
			d := newDemand562On(t, forge, 3, prs)
			d.status = "SUCCESSFUL"
			closed := map[string]string{"bitbucket.org": "DECLINED", "github.com": "CLOSED"}[forge]
			for interval := 0; interval < 4; interval++ {
				if interval > 0 {
					d.advance(statusOpenListAge(forge) + time.Second)
				}
				before := d.listReads()
				for pass := 0; pass < 3; pass++ {
					for _, snapshot := range d.pass() {
						for _, repo := range []string{"r1", "r2"} {
							if row := demand562Row(t, snapshot, repo, "11"); row.State != "OPEN" || row.Checks == nil || *row.Checks != "SUCCESS" || statusObservationUnavailable(row.Freshness) {
								t.Fatalf("interval %d pass %d: open row %+v", interval, pass, row)
							}
							if row := demand562Row(t, snapshot, repo, "12"); row.State != closed || statusObservationUnavailable(row.Freshness) {
								t.Fatalf("interval %d pass %d: closed row %+v", interval, pass, row)
							}
							if row := demand562Row(t, snapshot, repo, "13"); row.State != "MERGED" {
								t.Fatalf("interval %d pass %d: merged row %+v", interval, pass, row)
							}
						}
					}
				}
				if got := d.listReads() - before; got > len(repos) {
					t.Fatalf("interval %d: %d list reads by 3 panes over %d repositories", interval, got, len(repos))
				}
			}
			for _, repo := range []string{"r1", "r2"} {
				if got := d.reads("view", repo, "11"); got != 0 {
					t.Errorf("%s open PR read on its own %d times", repo, got)
				}
				for _, number := range []string{"12", "13"} {
					if got := d.reads("view", repo, number); got != 1 {
						t.Errorf("%s#%s read %d times, want once", repo, number, got)
					}
				}
			}
			// One status and one pipelines page per open head; passed checks
			// are not read again.
			if forge == "bitbucket.org" && d.checkReads() != 2*len(repos) {
				t.Errorf("check pages read %d times, want %d", d.checkReads(), 2*len(repos))
			}
		})
	}
}

// A PR opened and enlisted after its repository's list was read, within the
// list interval, is shown from a list read and not read on its own.
func TestStatusDemand562NewPRAppearsFromTheList(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		t.Run(forge, func(t *testing.T) {
			d := newDemand562On(t, forge, 2, map[string]map[string]string{"example/r1": {"11": "OPEN"}})
			d.pass()
			d.setPR("example/r1", "14", "OPEN")
			d.enlist("example/r1", "14")
			lists := d.listReads()
			for i, snapshot := range d.pass() {
				if row := demand562Row(t, snapshot, "r1", "14"); row.State != "OPEN" || statusObservationUnavailable(row.Freshness) {
					t.Fatalf("pane %d: new PR %+v", i, row)
				}
			}
			if got := d.reads("view", "r1", "14"); got != 0 {
				t.Fatalf("new PR read on its own %d times", got)
			}
			if got := d.listReads() - lists; got != 1 {
				t.Fatalf("%d list reads for two panes, want one", got)
			}
		})
	}
}

// A PR that merges leaves the open list, is read once to record the merge,
// and is not read again by later background passes or an explicit refresh.
func TestStatusDemand562MergedPRIsReadOnce(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		t.Run(forge, func(t *testing.T) {
			d := newDemand562On(t, forge, 2, map[string]map[string]string{"example/r1": {"11": "OPEN", "15": "OPEN"}})
			d.pass()
			d.setPR("example/r1", "11", "MERGED")
			for i := 0; i < 4; i++ {
				d.advance(statusOpenListAge(forge) + time.Second)
				for _, snapshot := range d.pass() {
					if row := demand562Row(t, snapshot, "r1", "11"); row.State != "MERGED" {
						t.Fatalf("pass %d: %+v", i, row)
					}
				}
			}
			if row := demand562Row(t, d.refresh(0), "r1", "11"); row.State != "MERGED" {
				t.Fatalf("explicit refresh: %+v", row)
			}
			if got := d.reads("view", "r1", "11"); got != 1 {
				t.Fatalf("merged PR read %d times, want once", got)
			}
			if record := demand562Record(t, d.panes[1], "11"); record.MergedOn == "" {
				t.Fatalf("merge not recorded in the second pane: %+v", record)
			}
		})
	}
}

// An explicit refresh reads every list of the pane again, although they are
// still current, and reads no PR on its own.
func TestStatusDemand562ExplicitRefreshRereadsListsNotPRs(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		t.Run(forge, func(t *testing.T) {
			prs := map[string]map[string]string{"example/r1": {"11": "OPEN", "13": "MERGED"}, "example/r2": {"21": "OPEN"}}
			d := newDemand562On(t, forge, 1, prs)
			d.pass()
			lists, views := d.listReads(), d.views()
			snapshot := d.refresh(0)
			for _, pr := range [][2]string{{"r1", "11"}, {"r2", "21"}} {
				if row := demand562Row(t, snapshot, pr[0], pr[1]); row.State != "OPEN" || row.Freshness != "live" || row.HeadCommit != demand562Head {
					t.Fatalf("explicit refresh row %+v", row)
				}
			}
			if got := d.listReads() - lists; got != 2 {
				t.Fatalf("explicit refresh read %d lists, want both", got)
			}
			if got := d.views() - views; got != 0 {
				t.Fatalf("explicit refresh read %d PRs on their own", got)
			}
		})
	}
}

// Bitbucket checks are kept per head commit. Passed or failed checks of an
// unchanged head are not read again by background passes; pending ones are,
// once per pass past the cache age. An explicit refresh reads them again, so
// a rerun on the same head shows.
func TestStatusDemand562SettledChecksAreNotReadAgain(t *testing.T) {
	for _, tc := range []struct {
		status  string
		settled bool
	}{{"SUCCESSFUL", true}, {"FAILED", true}, {"INPROGRESS", false}} {
		t.Run(tc.status, func(t *testing.T) {
			d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"11": "OPEN"}})
			d.status = tc.status
			const intervals = 4
			for i := 0; i < intervals; i++ {
				if i > 0 {
					d.advance(statusOpenListAge("bitbucket.org") + time.Second)
				}
				d.pass()
			}
			want := 2
			if !tc.settled {
				want = 2 * intervals
			}
			if got := d.checkReads(); got != want {
				t.Fatalf("check pages read %d times over %d intervals, want %d", got, intervals, want)
			}
			d.refresh(0)
			if got := d.checkReads(); got != want+2 {
				t.Fatalf("explicit refresh read %d check pages, want 2", got-want)
			}
		})
	}
}

// A merge whose checks are still running is read again once per list, by
// one pane, until its checks settle and it is recorded.
func TestStatusDemand562UnsettledMergeIsReadOncePerList(t *testing.T) {
	d := newDemand562On(t, "github.com", 3, map[string]map[string]string{"example/r1": {"11": "OPEN", "15": "OPEN"}})
	d.pass()
	d.setPR("example/r1", "11", "MERGED")
	pending := strings.Replace(demand562GHDetail("11", "MERGED"), `{"status":"COMPLETED","conclusion":"SUCCESS"}`, `{"status":"IN_PROGRESS","conclusion":""}`, 1)
	fixtureFile(t, filepath.Join(d.dir, "gh-r1-11.json"), pending, 0600)
	for i := 1; i <= 2; i++ {
		d.advance(statusOpenListAge("github.com") + time.Second)
		d.pass()
		if got := d.reads("view", "r1", "11"); got != i {
			t.Fatalf("list %d: merge read %d times by three panes, want %d", i, got, i)
		}
	}
	d.setPR("example/r1", "11", "MERGED")
	for i := 0; i < 3; i++ {
		d.advance(statusOpenListAge("github.com") + time.Second)
		d.pass()
	}
	if got := d.reads("view", "r1", "11"); got != 3 {
		t.Fatalf("settled merge read %d times, want 3", got)
	}
	for i, pane := range d.panes {
		if record := demand562Record(t, pane, "11"); record.MergedOn == "" {
			t.Fatalf("pane %d did not record the merge: %+v", i, record)
		}
	}
}

// With one token a minute, a PR never observed and missing from a fresh list
// gets the next token for its own read: the list that already lacked it is
// not read again for it.
func TestStatusDemand562NeverObservedPRIsReadWithOneToken(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"13": "MERGED"}})
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	if row := demand562Row(t, d.pass()[0], "r1", "13"); row.State == "MERGED" || row.ObservationReason != "shared-budget" {
		t.Fatalf("the list took the only token, so the PR waits: %+v", row)
	}
	foreground424Budget(t, "bitbucket.org", 1, time.Now())
	if row := demand562Row(t, d.pass()[0], "r1", "13"); row.State != "MERGED" {
		t.Fatalf("the refilled token did not reach the PR: %+v", row)
	}
	if lists, views := d.listReads(), d.reads("view", "r1", "13"); lists != 1 || views != 1 {
		t.Fatalf("%d list reads and %d PR reads, want one each", lists, views)
	}
}

func demand562Checks(row StatusPRRow) string {
	if row.Checks == nil {
		return ""
	}
	return *row.Checks
}

// When an explicit refresh's phase ends, a record whose list was read stays
// live, and a record whose list read is still waiting keeps its last
// observation, checks included, deferred with reason deadline. The snapshot
// is still saved.
func TestStatusDemand562PhaseDeadlineKeepsUnfinishedRecords(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"11": "OPEN"}, "example/r2": {"21": "OPEN"}})
	d.status = "SUCCESSFUL"
	last := demand562Row(t, d.pass()[0], "r2", "21")
	d.mu.Lock()
	d.stall = "example/r2"
	d.mu.Unlock()
	pane := d.panes[0]
	snapshot, err := pane.statusForegroundSnapshot(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if row := demand562Row(t, snapshot, "r1", "11"); row.State != "OPEN" || row.Freshness != "live" || demand562Checks(row) != "SUCCESS" {
		t.Fatalf("finished record: %+v checks=%q", row, demand562Checks(row))
	}
	row := demand562Row(t, snapshot, "r2", "21")
	if row.State != "OPEN" || row.Freshness != "deferred" || row.ObservationReason != "deadline" || row.ObservedAt != last.ObservedAt || demand562Checks(row) != "SUCCESS" {
		t.Fatalf("unfinished record: %+v checks=%q, last %+v", row, demand562Checks(row), last)
	}
	saved, err := pane.ReadStatusSnapshot()
	if err != nil || len(saved.PRs) != 2 {
		t.Fatalf("snapshot not saved: %+v %v", saved.PRs, err)
	}
}

// An explicit refresh admits every page of a Bitbucket list as it admits the
// first, waiting for a refill, so a PR on the second page is shown from the
// list, and later passes do not see a capped list.
func TestStatusDemand562ExplicitRefreshReadsEveryListPage(t *testing.T) {
	d := newDemand562(t, 1, map[string]map[string]string{"example/r1": {"11": "OPEN"}})
	d.paged = true
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "1")
	if row := demand562Row(t, d.refresh(0), "r1", "11"); row.State != "OPEN" || row.Freshness != "live" {
		t.Fatalf("explicit refresh: %+v", row)
	}
	if got := d.views(); got != 0 {
		t.Fatalf("PR read on its own %d times", got)
	}
	if row := demand562Row(t, d.pass()[0], "r1", "11"); row.State != "OPEN" || row.ObservationReason == "list-truncated" {
		t.Fatalf("background pass: %+v", row)
	}
}

// A GitHub close is not recorded, so a closed PR off the list is final for
// background passes, while an explicit refresh reads it once more: it can have
// been reopened and merged between two lists.
func TestStatusDemand562ExplicitRefreshRereadsAGitHubClose(t *testing.T) {
	d := newDemand562On(t, "github.com", 1, map[string]map[string]string{"example/r1": {"11": "OPEN", "12": "DECLINED"}})
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "CLOSED" {
		t.Fatalf("first pass: %+v", row)
	}
	d.setPR("example/r1", "12", "MERGED")
	d.advance(statusOpenListAge("github.com") + time.Second)
	views := d.reads("view", "r1", "12")
	if row := demand562Row(t, d.pass()[0], "r1", "12"); row.State != "CLOSED" || d.reads("view", "r1", "12") != views {
		t.Fatalf("background pass read the close again: %+v", row)
	}
	if row := demand562Row(t, d.refresh(0), "r1", "12"); row.State != "MERGED" {
		t.Fatalf("explicit refresh: %+v", row)
	}
	if got := d.reads("view", "r1", "12") - views; got != 1 {
		t.Fatalf("explicit refresh read the PR %d times, want once", got)
	}
	if record := demand562Record(t, d.panes[0], "12"); record.MergedOn == "" {
		t.Fatalf("merge not recorded: %+v", record)
	}
}

// A Bitbucket host without a REST credential has no open list, so status
// reads its PRs by number: in a known credential context (here TWG with an
// environment token) at most once per cache age across status operations,
// and at once under an explicit refresh.
func TestStatusDemand562NoRESTReadsAPROncePerCacheAge(t *testing.T) {
	h, _ := archive511TWG(t, strings.Replace(archive511Declined, `"DECLINED"`, `"OPEN"`, 1), archive511Entries())
	t.Setenv("TWG_BBC_TOKEN", "synthetic-twg-token")
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	pass := func(intent StatusRefreshIntent) {
		t.Helper()
		ctx := statusAuthOperation(context.Background())
		if _, _, err := bitbucketRESTCredentials(ctx, "acme/alpha"); !errors.Is(err, errBitbucketNoREST) {
			t.Fatalf("fixture has a REST credential: %v", err)
		}
		if detail := statusEnrichRecordMetadataContext(ctx, intent, PRRecord{Repo: "alpha", Number: "4"}, "acme/alpha", "bitbucket.org"); detail.State != "OPEN" {
			t.Fatalf("%+v", detail)
		}
	}
	for range 3 {
		pass(StatusRefreshBackground)
	}
	if got := archive511TWGReads(t, h, "get"); got != 1 {
		t.Fatalf("PR read %d times within one cache age, want once", got)
	}
	(&demand562{t: t}).advance(statusForgeCacheAge() + time.Second)
	pass(StatusRefreshBackground)
	if got := archive511TWGReads(t, h, "get"); got != 2 {
		t.Fatalf("PR read %d times after the cache age, want twice", got)
	}
	pass(StatusRefreshForeground)
	if got := archive511TWGReads(t, h, "get"); got != 3 {
		t.Fatalf("explicit refresh within the cache age made %d reads, want one", got-2)
	}
}

// Panes that refresh at the same time share one list read for a new PR: a
// pane that waited on another's read uses that list instead of reading again.
func TestStatusDemand562OverlappingPanesShareOneListRead(t *testing.T) {
	d := newDemand562On(t, "github.com", 3, map[string]map[string]string{"example/r1": {"11": "OPEN"}})
	d.pass()
	d.setPR("example/r1", "14", "OPEN")
	d.enlist("example/r1", "14")
	// A slow list read keeps the panes overlapped: each loads the older list
	// and asks for a new one before the first read returns.
	t.Setenv("WTC_TEST_LIST_DELAY", "2")
	lists := d.listReads()
	var wg sync.WaitGroup
	rows := make([]StatusPRRow, len(d.panes))
	errs := make([]error, len(d.panes))
	for i, pane := range d.panes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := pane.statusForgePreview(false, true)
			errs[i] = err
			for _, row := range snapshot.PRs {
				if row.Repo == "r1" && row.Number == "14" {
					rows[i] = row
				}
			}
		}()
	}
	wg.Wait()
	for i := range d.panes {
		if errs[i] != nil || rows[i].State != "OPEN" {
			t.Fatalf("pane %d: %+v %v", i, rows[i], errs[i])
		}
	}
	if got := d.listReads() - lists; got != 1 {
		t.Fatalf("%d list reads by %d overlapping panes, want one", got, len(d.panes))
	}
	if got := d.views(); got != 0 {
		t.Fatalf("new PR read on its own %d times", got)
	}
}
