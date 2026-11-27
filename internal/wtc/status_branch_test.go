package wtc

import (
	"context"
	"errors"
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

func TestStatusBranchListMatchesExactSourceBranch(t *testing.T) {
	gh := []byte(`[{"number":2,"headRefName":"other"},{"number":7,"headRefName":"topic"}]`)
	number, err := statusParseBranchList("github.com", gh, "topic")
	if err != nil || number != "7" {
		t.Fatalf("wrong GitHub branch match: %q %v", number, err)
	}
	bb := []byte(`{"pullRequests":[{"id":3,"source":{"branch":{"name":"other"}}},{"id":9,"source":{"branch":{"name":"topic"}}}]}`)
	number, err = statusParseBranchList("bitbucket.org", bb, "topic")
	if err != nil || number != "9" {
		t.Fatalf("wrong Bitbucket branch match: %q %v", number, err)
	}
	if number, err := statusParseBranchList("github.com", gh, "topic-more"); err != nil || number != "" {
		t.Fatalf("substring branch match: %q %v", number, err)
	}
	if _, err := statusParseBranchList("github.com", []byte(`{}`), "topic"); err == nil {
		t.Fatal("malformed GitHub list accepted as empty")
	}
}

func TestStatusBranchDiscoveryCachesOnlyValidAnswers(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	script := `#!/bin/sh
case "$2" in
  list) printf '[{"number":7,"headRefName":"topic"}]\n' ;;
  view)
    case "$*" in *mergeable,*) ;; *) exit 2 ;; esac
    printf '{"number":7,"state":"OPEN","title":"Synthetic PR","mergeable":"CONFLICTING","mergeStateStatus":"DRAFT","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}\n' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	number, err := statusDiscoverBranch("github.com", "example/widget", "topic")
	if err != nil || number != "7" {
		t.Fatalf("discovery failed: %q %v", number, err)
	}
	detail := statusEnrichRecord(PRRecord{Number: number}, "example/widget", "github.com")
	if detail.State != "OPEN" || detail.Checks != "SUCCESS" || detail.Merge != "CONFLICTING" || detail.Title != "Synthetic PR" {
		t.Fatalf("enrichment failed: %+v", detail)
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if cached, err := statusDiscoverBranch("github.com", "example/widget", "topic"); err != nil || cached != number {
		t.Fatalf("valid discovery cache missed: %q %v", cached, err)
	}
	if cached := statusEnrichRecord(PRRecord{Number: number}, "example/widget", "github.com"); cached.State != "OPEN" {
		t.Fatalf("valid detail cache missed: %+v", cached)
	}
	if number, err := statusDiscoverBranch("github.com", "example/widget", "different"); err != nil || number != "" {
		t.Fatalf("branch absent from a successful list: %q %v", number, err)
	}
	if _, err := statusDiscoverBranch("github.com", "example/other", "topic"); err == nil {
		t.Fatal("failed discovery was treated as a negative result")
	}
}

func TestStatusBitbucketDiscoveryReadsPastFirstPage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var sawFields bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("fields"), "updated_on") && strings.Contains(r.URL.Query().Get("fields"), "comment_count") {
			sawFields = true
		}
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`{"values":[{"id":99,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"topic"}}}]}`))
			return
		}
		items := make([]string, 50)
		for i := range items {
			items[i] = fmt.Sprintf(`{"id":%d,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"other-%d"}}}`, i+1, i)
		}
		fmt.Fprintf(w, `{"values":[%s],"next":"http://%s%s?page=2"}`, strings.Join(items, ","), r.Host, r.URL.Path)
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	number, err := statusDiscoverBranch("bitbucket.org", "example/widget", "topic")
	if err != nil || number != "99" || !sawFields {
		t.Fatalf("branch beyond first page was missed: %q %v fields %v", number, err, sawFields)
	}
}

func TestStatusBitbucketDiscoverySharesOneList(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"values":[{"id":7,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"topic"}}},{"id":8,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"other"}}}]}`))
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	topic, err := statusDiscoverBranch("bitbucket.org", "example/widget", "topic")
	other, otherErr := statusDiscoverBranch("bitbucket.org", "example/widget", "other")
	missing, missingErr := statusDiscoverBranch("bitbucket.org", "example/widget", "absent")
	if err != nil || otherErr != nil || missingErr != nil || topic != "7" || other != "8" || missing != "" || hits.Load() != 1 {
		t.Fatalf("two branches did not share one list: %q %q %q hits=%d errs=%v %v %v", topic, other, missing, hits.Load(), err, otherErr, missingErr)
	}
}

func TestStatusBitbucketListChargesEveryPage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "60")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("page") == "2" {
			w.Write([]byte(`{"values":[{"id":2,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"other"}}}]}`))
			return
		}
		fmt.Fprintf(w, `{"values":[{"id":1,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"topic"}}}],"next":"http://%s%s?page=2"}`, r.Host, r.URL.Path)
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	listed, truncated, err := statusOpenPRs("bitbucket.org", "example/widget")
	if err != nil || !truncated || hits.Load() != 1 || listed["1"].Number != "1" || listed["2"].Number != "" {
		t.Fatalf("second page was not charged: listed=%v truncated=%v hits=%d err=%v", listed, truncated, hits.Load(), err)
	}
}

func TestStatusBitbucketDiscoveryLooksUpABranchTheCappedListOmits(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_USERNAME", "builder")
	t.Setenv("BB_API_TOKEN", "token")
	var lists, branches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "" {
			branches.Add(1)
			if !strings.Contains(r.URL.Query().Get("q"), "topic") {
				t.Errorf("branch query = %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"values":[{"id":9,"source":{"branch":{"name":"topic"}}}]}`))
			return
		}
		lists.Add(1)
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		var n int
		fmt.Sscan(page, &n)
		if n < 11 {
			fmt.Fprintf(w, `{"values":[{"id":%d,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"other-%d"}}}],"next":"http://%s%s?page=%d"}`, n, n, r.Host, r.URL.Path, n+1)
			return
		}
		fmt.Fprintf(w, `{"values":[{"id":%d,"state":"OPEN","updated_on":"2026-10-01T00:00:00Z","source":{"branch":{"name":"other-%d"}}}]}`, n, n)
	}))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	number, err := statusDiscoverBranch("bitbucket.org", "example/widget", "topic")
	if err != nil || number != "9" || lists.Load() != 10 || branches.Load() != 1 {
		t.Fatalf("capped list did not look up the branch: %q lists=%d branches=%d err=%v", number, lists.Load(), branches.Load(), err)
	}
	again, err := statusDiscoverBranch("bitbucket.org", "example/widget", "other-1")
	if err != nil || again != "1" || lists.Load() != 10 || branches.Load() != 1 {
		t.Fatalf("branch on the capped list was looked up again: %q lists=%d branches=%d err=%v", again, lists.Load(), branches.Load(), err)
	}
}

func TestStatusGitHubDiscoveryFallsBackWhenTheListIsFull(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var lists, heads int
	bin := t.TempDir()
	var items []string
	for i := 0; i <= statusOpenListLimit; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"headRefName":"other-%d","state":"OPEN","title":"Other"}`, i+1, i))
	}
	full := "[" + strings.Join(items, ",") + "]"
	script := "#!/bin/sh\ncase \"$*\" in\n  *--head*) printf 'head\\n' >> \"$WTC_TEST_HEADS\"; printf '[{\"number\":9,\"headRefName\":\"topic\"}]\\n' ;;\n  *) printf 'list\\n' >> \"$WTC_TEST_LISTS\"; printf '%s\\n' '" + full + "' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WTC_TEST_LISTS", filepath.Join(t.TempDir(), "lists"))
	t.Setenv("WTC_TEST_HEADS", filepath.Join(t.TempDir(), "heads"))
	number, err := statusDiscoverBranch("github.com", "example/widget", "topic")
	if err != nil || number != "9" {
		t.Fatalf("full list did not fall back to the branch: %q %v", number, err)
	}
	other, err := statusDiscoverBranch("github.com", "example/widget", "other-0")
	if err != nil || other != "1" {
		t.Fatalf("branch on the full list was missed: %q %v", other, err)
	}
	lists = statusTestFileLines(t, os.Getenv("WTC_TEST_LISTS"))
	heads = statusTestFileLines(t, os.Getenv("WTC_TEST_HEADS"))
	if lists != 1 || heads != 1 {
		t.Fatalf("list calls=%d head calls=%d", lists, heads)
	}
}

func TestStatusCappedListDoesNotRefetchAnOpenPullRequest(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	views := filepath.Join(t.TempDir(), "views")
	t.Setenv("WTC_TEST_GH_VIEWS", views)
	var items []string
	for i := 0; i <= statusOpenListLimit; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"state":"OPEN","title":"Other","headRefName":"other-%d"}`, i+101, i))
	}
	script := "#!/bin/sh\ncase \"$2\" in\n  list) printf '%s\\n' '[" + strings.Join(items, ",") + "]' ;;\n  view) printf 'viewed\\n' >> \"$WTC_TEST_GH_VIEWS\"; printf '%s\\n' '{\"number\":7,\"state\":\"OPEN\",\"title\":\"Still open\"}' ;;\n  *) exit 2 ;;\nesac\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	statusWriteForgeCache("github.com", "example/widget", "7", []byte(`{"number":7,"state":"OPEN","title":"Saved"}`))
	detail := statusEnrichRecord(PRRecord{Number: "7"}, "example/widget", "github.com")
	if detail.Title != "Saved" || statusTestFileLines(t, views) != 0 {
		t.Fatalf("capped list refetched an open pull request: %+v views=%d", detail, statusTestFileLines(t, views))
	}
}

func TestStatusCappedListDoesNotReadPRsBeyondIt(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	views := filepath.Join(t.TempDir(), "views")
	t.Setenv("WTC_TEST_GH_VIEWS", views)
	// 150 PRs are open; like gh, the fake returns at most --limit of them.
	var items []string
	for i := 0; i < 150; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"state":"OPEN","title":"Other","headRefName":"other-%d"}`, i+101, i))
	}
	open := filepath.Join(t.TempDir(), "open")
	fixtureFile(t, open, strings.Join(items, "\n")+"\n", 0600)
	t.Setenv("WTC_TEST_GH_OPEN", open)
	script := "#!/bin/sh\nlimit=30 prev=\nfor arg in \"$@\"; do [ \"$prev\" = --limit ] && limit=$arg; prev=$arg; done\ncase \"$2\" in\n  list) printf '['; head -n \"$limit\" \"$WTC_TEST_GH_OPEN\" | paste -sd, -; printf ']\\n' ;;\n  view) printf 'viewed\\n' >> \"$WTC_TEST_GH_VIEWS\"; printf '%s\\n' '{\"number\":7,\"state\":\"OPEN\",\"title\":\"Audited\"}' ;;\n  *) exit 2 ;;\nesac\n"
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	statusWriteForgeCache("github.com", "example/widget", "7", []byte(`{"number":7,"state":"OPEN","title":"Saved"}`))
	// A list cut short at its limit says nothing about a PR beyond it: the PR
	// keeps its last observation, not current, and is not read on its own.
	for pass := 0; pass < 2; pass++ {
		detail := statusEnrichRecord(PRRecord{Number: "7"}, "example/widget", "github.com")
		if detail.Title != "Saved" || detail.Freshness != "deferred" || detail.Reason != "list-truncated" || statusTestFileLines(t, views) != 0 {
			t.Fatalf("pass %d beyond a capped list: %+v views=%d", pass, detail, statusTestFileLines(t, views))
		}
	}
}

func TestStatusSingleFlightFetchesOnce(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func() ([]byte, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return []byte(`{"ok":true}`), nil
	}
	errc := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := statusSingleFlight("probe", "github.com", "example/widget", "shared", fetch)
			errc <- err
		}()
	}
	<-started
	select {
	case <-started:
		t.Fatal("two fetches ran at once")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestStatusForgePreviewFindsUnenlistedBranchPR(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	gh := filepath.Join(bin, "gh")
	script := `#!/bin/sh
case "$2" in
  list) printf '[{"number":7,"headRefName":"topic"}]\n' ;;
  view) printf '{"number":7,"state":"OPEN","title":"Synthetic PR","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}\n' ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	c := newWorkspaceFixture(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c.Registry.Repos[0].Remote = "https://github.com/example/agent-harness.git"
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.PRs) != 1 || !snapshot.PRs[0].NotEnlisted {
		t.Fatalf("unenlisted PR missing discovery provenance: %+v", snapshot.PRs)
	}
	for _, row := range snapshot.Repos {
		if row.Dir == "harness" {
			if row.PR == nil || row.PR.Number != "7" || row.PR.Checks != "SUCCESS" {
				t.Fatalf("open branch PR not shown inline: %+v", row.PR)
			}
			return
		}
	}
	t.Fatal("harness row missing")
}

func TestStatusBranchDiscoveryPreservesOperationCancellation(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			ctx = statusAuthOperation(ctx)
			cancel()
			if _, err := statusDiscoverBranchContext(ctx, forge, "example/widget", "topic"); !errors.Is(err, context.Canceled) {
				t.Fatalf("discovery lost cancellation: %v", err)
			}
			var err error
			if forge == "github.com" {
				_, err = statusDiscoverGitHubBranchContext(ctx, "example/widget", "topic")
			} else {
				_, err = statusDiscoverBitbucketBranchContext(ctx, "example/widget", "topic")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fallback lost cancellation: %v", err)
			}
		})
	}
}

func TestStatusBranchFallbackProviderDeadline(t *testing.T) {
	auth441Fixture(t)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\nexec sleep 5\n", 0700)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("BB_API_USER", "synthetic-user")
	t.Setenv("BB_API_PASS", "synthetic-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	old := statusBBAPI
	statusBBAPI = server.URL
	defer func() { statusBBAPI = old }()
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			var err error
			if forge == "github.com" {
				_, err = statusDiscoverGitHubBranchContext(ctx, "example/widget", "topic")
			} else {
				_, err = statusDiscoverBitbucketBranchContext(ctx, "example/widget", "topic")
			}
			if err == nil || ctx.Err() != context.DeadlineExceeded || time.Since(started) > 2*time.Second {
				t.Fatalf("provider fallback did not honor deadline: %v (%v)", err, time.Since(started))
			}
		})
	}
}

func TestStatusGitHub562ListOfExactlyTheLimitIsComplete(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	bin := t.TempDir()
	var items []string
	for i := 0; i < statusOpenListLimit; i++ {
		items = append(items, fmt.Sprintf(`{"number":%d,"headRefName":"b-%d","state":"OPEN","title":"T"}`, i+1, i))
	}
	script := "#!/bin/sh\nprintf '%s\\n' '[" + strings.Join(items, ",") + "]'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	listed, truncated, err := statusOpenPRs("github.com", "example/widget")
	if err != nil || truncated || len(listed) != statusOpenListLimit {
		t.Fatalf("a list of exactly the limit: listed=%d truncated=%v err=%v", len(listed), truncated, err)
	}
}
