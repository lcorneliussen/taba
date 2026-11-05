package wtc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func statusHideBitbucketLogin(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"BB_API_USER", "BB_API_PASS", "BB_USERNAME", "BB_API_TOKEN", "ATLASSIAN_ACCOUNT_EMAIL", "ATLASSIAN_API_TOKEN"} {
		t.Setenv(name, "")
	}
}

func TestStatusListFingerprintNoticesCommentsAndChecks(t *testing.T) {
	bb := statusBBListItem{ID: 9, UpdatedOn: "2026-10-01T00:00:00Z", CommentCount: 1, TaskCount: 0, State: "OPEN", Title: "Listed"}
	if bb.fingerprint() != bb.fingerprint() {
		t.Fatal("Bitbucket fingerprint is unstable")
	}
	commented := bb
	commented.CommentCount = 2
	if commented.fingerprint() == bb.fingerprint() {
		t.Fatal("comment count was left out of the Bitbucket fingerprint")
	}
	gh := statusGHListItem{Number: 7, UpdatedAt: "2026-10-01T00:00:00Z", State: "OPEN", Title: "Listed"}
	gh.StatusCheckRollup = append(gh.StatusCheckRollup, struct {
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
		Status     string `json:"status"`
	}{Conclusion: "SUCCESS", Status: "COMPLETED"})
	failed := gh
	failed.StatusCheckRollup = append([]struct {
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
		Status     string `json:"status"`
	}{}, gh.StatusCheckRollup...)
	failed.StatusCheckRollup[0].Conclusion = "FAILURE"
	if gh.fingerprint() == failed.fingerprint() {
		t.Fatal("check conclusion was left out of the GitHub fingerprint")
	}
}

func TestStatusOpenListSkipsUnchangedGitHubPullRequests(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	listPath := filepath.Join(t.TempDir(), "list.json")
	views := filepath.Join(t.TempDir(), "views")
	lists := filepath.Join(t.TempDir(), "lists")
	open := `[{"number":7,"title":"Listed","state":"OPEN","updatedAt":"2026-10-01T00:00:00Z","reviewDecision":"APPROVED","headRefName":"topic","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]},{"number":8,"title":"Also","state":"OPEN","updatedAt":"2026-10-01T00:00:00Z","headRefName":"other","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}]`
	if err := os.WriteFile(listPath, []byte(open), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WTC_TEST_GH_LIST", listPath)
	t.Setenv("WTC_TEST_GH_VIEWS", views)
	t.Setenv("WTC_TEST_GH_LISTS", lists)
	script := `#!/bin/sh
case "$2" in
  list) printf 'listed\n' >> "$WTC_TEST_GH_LISTS"; cat "$WTC_TEST_GH_LIST" ;;
  view) printf 'viewed\n' >> "$WTC_TEST_GH_VIEWS"; printf '%s\n' '{"number":7,"state":"MERGED","mergedAt":"2026-10-02T00:00:00Z","title":"Merged"}' ;;
  *) exit 2 ;;
esac
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	slug := "example/widget"
	first := statusEnrichRecord(PRRecord{Number: "7"}, slug, "github.com")
	second := statusEnrichRecord(PRRecord{Number: "8"}, slug, "github.com")
	if first.Title != "Listed" || first.Checks != "SUCCESS" || first.Review != "approved" || second.Title != "Also" {
		t.Fatalf("list did not supply both pull requests: %+v %+v", first, second)
	}
	if count := statusTestFileLines(t, lists); count != 1 || statusTestFileLines(t, views) != 0 {
		t.Fatalf("open list was not shared, lists=%d views=%d", count, statusTestFileLines(t, views))
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeEntryPath("openprs", "github.com", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	if again := statusEnrichRecord(PRRecord{Number: "7"}, slug, "github.com"); again.Title != "Listed" || again.Checks != "SUCCESS" {
		t.Fatalf("unchanged list replaced the saved detail: %+v", again)
	}
	if statusTestFileLines(t, views) != 0 || statusTestFileLines(t, lists) != 2 {
		t.Fatalf("unchanged pull request was viewed, lists=%d views=%d", statusTestFileLines(t, lists), statusTestFileLines(t, views))
	}
	failed := `[{"number":7,"title":"Listed","state":"OPEN","updatedAt":"2026-10-03T00:00:00Z","reviewDecision":"APPROVED","headRefName":"topic","statusCheckRollup":[{"conclusion":"FAILURE","status":"COMPLETED"}]}]`
	if err := os.WriteFile(listPath, []byte(failed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(statusForgeEntryPath("openprs", "github.com", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	if updated := statusEnrichRecord(PRRecord{Number: "7"}, slug, "github.com"); updated.Checks != "FAILURE" || statusTestFileLines(t, views) != 0 {
		t.Fatalf("changed checks did not come from the list: %+v views=%d", updated, statusTestFileLines(t, views))
	}
	if err := os.WriteFile(listPath, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(statusForgeEntryPath("openprs", "github.com", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	merged := statusEnrichRecord(PRRecord{Number: "7"}, slug, "github.com")
	if merged.State != "MERGED" || merged.MergedOn != "2026-10-02T00:00:00Z" || statusTestFileLines(t, views) != 1 {
		t.Fatalf("leaving the open list did not fetch the pull request: %+v views=%d", merged, statusTestFileLines(t, views))
	}
}

func TestStatusOpenListSkipsUnchangedBitbucketPullRequests(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var phase atomic.Int32
	var listHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listHits.Add(1)
		fields := r.URL.Query().Get("fields")
		if !strings.Contains(fields, "updated_on") || !strings.Contains(fields, "comment_count") || !strings.Contains(fields, "task_count") {
			t.Errorf("list is missing comparison fields: %s", r.URL.RawQuery)
		}
		switch phase.Load() {
		case 0, 1:
			fmt.Fprint(w, bitbucketOpenPR(0, "Listed"))
		case 2:
			fmt.Fprint(w, bitbucketOpenPR(2, "Commented"))
		default:
			fmt.Fprint(w, `{"values":[]}`)
		}
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	views := filepath.Join(t.TempDir(), "views")
	t.Setenv("WTC_TEST_BB_VIEWS", views)
	script := `#!/bin/sh
case "$2" in
  view) printf 'viewed\n' >> "$WTC_TEST_BB_VIEWS"; printf '%s\n' '{"id":9,"state":"MERGED","merged_on":"2026-10-02T00:00:00Z","title":"Merged"}' ;;
  *) exit 2 ;;
esac
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "bb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	slug := "example/widget"
	first := statusEnrichRecord(PRRecord{Number: "9"}, slug, "bitbucket.org")
	if first.Title != "Listed" || first.Review != "approved" || first.State != "OPEN" || listHits.Load() != 1 || statusTestFileLines(t, views) != 0 {
		t.Fatalf("list did not replace the view: %+v hits=%d views=%d", first, listHits.Load(), statusTestFileLines(t, views))
	}
	phase.Store(1)
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeEntryPath("openprs", "bitbucket.org", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	if again := statusEnrichRecord(PRRecord{Number: "9"}, slug, "bitbucket.org"); again.Title != "Listed" || listHits.Load() != 2 || statusTestFileLines(t, views) != 0 {
		t.Fatalf("unchanged pull request was viewed: %+v hits=%d views=%d", again, listHits.Load(), statusTestFileLines(t, views))
	}
	phase.Store(2)
	if err := os.Chtimes(statusForgeEntryPath("openprs", "bitbucket.org", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	if updated := statusEnrichRecord(PRRecord{Number: "9"}, slug, "bitbucket.org"); updated.Title != "Commented" || statusTestFileLines(t, views) != 0 {
		t.Fatalf("comment change did not come from the list: %+v views=%d", updated, statusTestFileLines(t, views))
	}
	phase.Store(3)
	if err := os.Chtimes(statusForgeEntryPath("openprs", "bitbucket.org", slug, "open"), past, past); err != nil {
		t.Fatal(err)
	}
	merged := statusEnrichRecord(PRRecord{Number: "9"}, slug, "bitbucket.org")
	if merged.State != "MERGED" || merged.MergedOn != "2026-10-02T00:00:00Z" || statusTestFileLines(t, views) != 1 {
		t.Fatalf("leaving the open list did not fetch the pull request: %+v views=%d", merged, statusTestFileLines(t, views))
	}
}

func TestStatusExpiredBitbucketDraftRefreshesAfterListOmission(t *testing.T) {
	for _, base := range []string{"main", "beta"} {
		for _, capped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/capped=%v", base, capped), func(t *testing.T) {
				c := newWorkspaceFixture(t)
				t.Setenv("TMPDIR", t.TempDir())
				statusHideBitbucketLogin(t)
				t.Setenv("WTC_CONFIG_ROOT", c.ConfigRoot)
				t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
				t.Setenv("WTC_FORGE_CACHE_AGE", "")
				t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
				t.Setenv("BB_USERNAME", "synthetic")
				t.Setenv("BB_API_TOKEN", "synthetic")
				owner := filepath.Join(c.Workspace, ".bare", "agent-harness.git")
				commit := fixtureGit(t, "-C", c.Harness, "rev-parse", "HEAD")
				fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "git@bitbucket.org:example/widget.git")
				fixtureGit(t, "--git-dir="+owner, "update-ref", "refs/remotes/origin/"+base, commit)
				fixtureGit(t, "-C", c.Harness, "switch", "-c", "topic")
				fixtureFile(t, filepath.Join(c.Harness, ".harness-repos.yml"),
					"repos:\n  - name: agent-harness\n    remote: git@bitbucket.org:example/widget.git\n    default_ref: origin/"+base+"\n", 0600)
				fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"),
					"agent-harness 7 topic https://bitbucket.org/example/widget/pull-requests/7 synthetic\n", 0600)
				var err error
				c, err = OpenCollection(c.Collection)
				if err != nil {
					t.Fatal(err)
				}
				open := fmt.Sprintf(`{"id":7,"state":"OPEN","draft":true,"title":"Synthetic draft","updated_on":"2026-11-04T10:00:00Z","source":{"commit":{"hash":%q},"branch":{"name":"topic"}},"destination":{"commit":{"hash":%q},"branch":{"name":%q}}}`, commit, commit, base)
				merged := fmt.Sprintf(`{"id":7,"state":"MERGED","draft":false,"title":"Synthetic merged","merged_on":"2026-11-04T10:01:00Z","source":{"commit":{"hash":%q}},"destination":{"commit":{"hash":%q},"branch":{"name":%q}},"merge_commit":{"hash":%q}}`, commit, commit, base, commit)
				var phase atomic.Int32
				var apiBase string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasSuffix(r.URL.Path, "/pullrequests") {
						fmt.Fprint(w, `{"values":[]}`)
						return
					}
					if phase.Load() == 0 {
						fmt.Fprintf(w, `{"values":[%s]}`, open)
						return
					}
					if !capped {
						fmt.Fprint(w, `{"values":[]}`)
						return
					}
					var page int
					fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
					fmt.Fprintf(w, `{"values":[{"id":%d,"state":"OPEN","draft":false,"source":{"branch":{"name":"other"}}}],"next":%q}`,
						100+page, fmt.Sprintf("%s/repositories/example/widget/pullrequests?page=%d", apiBase, page+1))
				}))
				defer server.Close()
				apiBase = server.URL
				old := statusBBAPI
				statusBBAPI = server.URL
				defer func() { statusBBAPI = old }()
				dir := t.TempDir()
				view, views := filepath.Join(dir, "view.json"), filepath.Join(dir, "views")
				fixtureFile(t, view, open, 0600)
				t.Setenv("WTC_TEST_BB_VIEW", view)
				t.Setenv("WTC_TEST_BB_VIEWS", views)
				fixtureFile(t, filepath.Join(dir, "bb"), `#!/bin/sh
case "$2" in
  view) printf 'view\n' >> "$WTC_TEST_BB_VIEWS"; cat "$WTC_TEST_BB_VIEW" ;;
  *) exit 77 ;;
esac
`, 0700)
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				first, err := c.statusForgePreview(false, false)
				if err != nil || len(first.PRs) != 1 || first.PRs[0].State != "DRAFT" {
					t.Fatalf("initial draft: %+v %v", first, err)
				}
				initialViews := statusTestFileLines(t, views)
				phase.Store(1)
				fixtureFile(t, view, merged, 0600)
				fixtureGit(t, "-C", c.Harness, "checkout", "--detach", "origin/"+base)
				fixtureGit(t, "-C", c.Harness, "branch", "-d", "topic")
				past := time.Now().Add(-2 * time.Minute)
				for kind, key := range map[string]string{"pr": "7", "openprs": "open"} {
					if err := os.Chtimes(statusForgeEntryPath(kind, "bitbucket.org", "example/widget", key), past, past); err != nil {
						t.Fatal(err)
					}
				}
				got, err := c.statusForgePreview(false, false)
				if err != nil || len(got.PRs) != 1 {
					t.Fatalf("merged snapshot: %+v %v", got, err)
				}
				row := got.PRs[0]
				if row.State != "MERGED" || row.Draft || row.MergedOn == nil || row.OnBranch || row.Base != base {
					t.Fatalf("expired draft survived list omission: %+v", row)
				}
				if count := statusTestFileLines(t, views); count != initialViews+1 {
					t.Fatalf("by-number refreshes=%d, want one after %d initial reads", count, initialViews)
				}
				again, err := c.statusForgePreview(false, false)
				if err != nil || len(again.PRs) != 1 || again.PRs[0].State != "MERGED" || statusTestFileLines(t, views) != initialViews+1 {
					t.Fatal("settled merge was refetched or changed", again, err)
				}
			})
		}
	}
}

func TestStatusCappedPRCacheRespectsFreshnessAndAllowance(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cached, fresh bool
		draft         bool
		mode, want    string
		views         int
	}{
		{"fresh-draft-budget-denied", true, true, true, "budget", "DRAFT", 0},
		{"fresh-open-budget-denied", true, true, false, "budget", "OPEN", 0},
		{"expired-draft-budget-denied", true, false, true, "budget", "", 0},
		{"expired-draft-cooling", true, false, true, "cooling", "", 0},
		{"expired-draft-view-failure", true, false, true, "error", "UNKNOWN", 1},
		{"uncached-view-failure", false, false, false, "error", "UNKNOWN", 1},
		{"uncached-malformed-view", false, false, false, "malformed", "UNKNOWN", 1},
		{"uncached-cooling", false, false, false, "cooling", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			statusHideBitbucketLogin(t)
			t.Setenv("WTC_FORGE_CACHE_AGE", "")
			t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
			forge, slug := "bitbucket.org", "example/widget"
			raw, err := statusEncodeOpenPRs(nil, true, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			statusWriteForgeEntry("openprs", forge, slug, "open", raw)
			statusMarkFullRefresh(forge, slug, "7")
			if tc.cached {
				statusWriteForgeCache(forge, slug, "7", []byte(fmt.Sprintf(`{"id":7,"state":"OPEN","draft":%t,"destination":{"branch":{"name":"beta"}}}`, tc.draft)))
				if !tc.fresh {
					past := time.Now().Add(-2 * time.Minute)
					if err := os.Chtimes(statusForgeEntryPath("pr", forge, slug, "7"), past, past); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.mode == "budget" {
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
				if !statusTakeForgeToken(forge) {
					t.Fatal("could not consume synthetic allowance")
				}
			}
			if tc.mode == "cooling" {
				statusForgeCool(forge, time.Hour)
			}
			dir := t.TempDir()
			views := filepath.Join(dir, "views")
			t.Setenv("WTC_TEST_BB_VIEWS", views)
			body, exit := `{"id":7,"state":"MERGED","merged_on":"2026-11-04T10:01:00Z"}`, "0"
			if tc.mode == "error" {
				exit = "1"
			}
			if tc.mode == "malformed" {
				body = "not-json"
			}
			fixtureFile(t, filepath.Join(dir, "view.json"), body, 0600)
			t.Setenv("WTC_TEST_BB_VIEW", filepath.Join(dir, "view.json"))
			fixtureFile(t, filepath.Join(dir, "bb"), "#!/bin/sh\nprintf 'view\\n' >> \"$WTC_TEST_BB_VIEWS\"\ncat \"$WTC_TEST_BB_VIEW\"\nexit "+exit+"\n", 0700)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			detail := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			if detail.State != tc.want || (tc.want == "" && detail.Checks != "PENDING") {
				t.Fatalf("unavailable or fresh facts misrepresented: %+v, want state=%q", detail, tc.want)
			}
			if count := statusTestFileLines(t, views); count != tc.views {
				t.Fatalf("by-number calls=%d, want %d", count, tc.views)
			}
		})
	}
}

func TestStatusCappedExpiredPRLookupIsShared(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge, slug := "bitbucket.org", "example/widget"
	raw, err := statusEncodeOpenPRs(nil, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	statusWriteForgeEntry("openprs", forge, slug, "open", raw)
	statusMarkFullRefresh(forge, slug, "7")
	statusWriteForgeCache(forge, slug, "7", []byte(`{"id":7,"state":"OPEN","draft":true}`))
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(statusForgeEntryPath("pr", forge, slug, "7"), past, past); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	views, started, release := filepath.Join(dir, "views"), filepath.Join(dir, "started"), filepath.Join(dir, "release")
	defer os.WriteFile(release, []byte("release"), 0600)
	t.Setenv("WTC_TEST_BB_VIEWS", views)
	t.Setenv("WTC_TEST_BB_STARTED", started)
	t.Setenv("WTC_TEST_BB_RELEASE", release)
	fixtureFile(t, filepath.Join(dir, "bb"), `#!/bin/sh
printf 'view\n' >> "$WTC_TEST_BB_VIEWS"
touch "$WTC_TEST_BB_STARTED"
while [ ! -f "$WTC_TEST_BB_RELEASE" ]; do sleep 0.01; done
printf '%s\n' '{"id":7,"state":"MERGED","merged_on":"2026-11-04T10:01:00Z"}'
`, 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ready, begin, results := make(chan struct{}, 2), make(chan struct{}), make(chan statusPRDetail, 2)
	for range 2 {
		go func() {
			ready <- struct{}{}
			<-begin
			results <- statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
		}()
	}
	<-ready
	<-ready
	close(begin)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("expired detail did not start its shared lookup")
	}
	fixtureFile(t, release, "release", 0600)
	for range 2 {
		select {
		case detail := <-results:
			if detail.State != "MERGED" {
				t.Fatalf("shared refresh returned %+v", detail)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("shared lookup did not finish")
		}
	}
	if count := statusTestFileLines(t, views); count != 1 || statusTakeForgeToken(forge) {
		t.Fatalf("coalesced lookup used %d views or bypassed its single-token allowance", count)
	}
}

func bitbucketOpenPR(comments int, title string) string {
	return fmt.Sprintf(`{"values":[{"id":9,"state":"OPEN","draft":false,"title":%q,"updated_on":"2026-10-01T00:00:00Z","comment_count":%d,"task_count":0,"source":{"branch":{"name":"topic"}},"destination":{"branch":{"name":"main"}},"participants":[{"approved":true,"state":"approved"}]}]}`, title, comments)
}

func statusTestFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	return lines
}
