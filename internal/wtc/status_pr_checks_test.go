package wtc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatusPRPipelineFailureReplacesOlderBranchBuild(t *testing.T) {
	raw := []byte(`{"values":[
 {"build_number":12,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"ref_name":"topic","selector":{"type":"pullrequests"},"commit":{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}},
 {"build_number":11,"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"ref_name":"topic","selector":{"type":"branches"},"commit":{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}]}`)
	build, err := statusParseBBBuild(raw, "example/widget", "topic")
	if err != nil || build == nil || *build.Build != "12" || *build.Checks != "FAILURE" || *build.SHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("current PR failure hidden by older branch build: %+v, %v", build, err)
	}
}

func setupHeadCheckServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BB_API_USER", "builder")
	t.Setenv("BB_API_PASS", "synthetic-token")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	server := httptest.NewServer(handler)
	old := statusBBAPI
	statusBBAPI = server.URL
	t.Cleanup(func() { statusBBAPI = old; server.Close() })
	return server
}

const checkHeadA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const checkHeadB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestStatusPRChecksExactHeadPaginationCacheAndFailurePrecedence(t *testing.T) {
	hits := 0
	var server *httptest.Server
	server = setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != "GET" {
			t.Errorf("unexpected mutation: %s", r.Method)
		}
		if strings.Contains(r.URL.Path, "/commit/") {
			if !strings.Contains(r.URL.Path, checkHeadA) && !strings.Contains(r.URL.Path, checkHeadB) {
				t.Errorf("not exact-head: %s", r.URL.Path)
			}
			if strings.Contains(r.URL.Path, checkHeadA) {
				fmt.Fprint(w, `{"values":[{"state":"FAILED"}]}`)
			} else {
				fmt.Fprint(w, `{"values":[{"state":"SUCCESSFUL"}]}`)
			}
			return
		}
		head := r.URL.Query().Get("target.commit.hash")
		if head == "" {
			t.Error("missing exact-head pipeline filter")
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `{"values":[{"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"commit":{"hash":%q},"selector":{"type":"pullrequests","pattern":"**"}}}]}`, head)
			return
		}
		fmt.Fprintf(w, `{"values":[{"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"commit":{"hash":%q},"selector":{"type":"branches"}}}],"next":%q}`, head, server.URL+"/pipelines/?page=2&target.commit.hash="+head)
	})
	first := statusBBHeadChecks("example/widget", checkHeadB, false)
	if first.Result != "FAILURE" || !first.Complete || first.Head != checkHeadB || hits != 3 {
		t.Fatalf("head B: %+v hits=%d", first, hits)
	}
	_ = statusBBHeadChecks("example/widget", checkHeadB, false)
	if hits != 3 {
		t.Fatalf("head cache missed: %d", hits)
	}
	old := statusBBHeadChecks("example/widget", checkHeadA, false)
	if old.Head != checkHeadA || old.Result != "FAILURE" || hits != 6 {
		t.Fatalf("head isolation: %+v hits=%d", old, hits)
	}
	fresh := statusBBHeadChecks("example/widget", checkHeadB, true)
	if fresh.Result != "FAILURE" || hits != 9 {
		t.Fatalf("review must refresh: %+v hits=%d", fresh, hits)
	}
}

func TestStatusPRChecksPartialEvidenceCannotPass(t *testing.T) {
	for _, state := range []string{"SUCCESSFUL", "FAILED", "mystery"} {
		t.Run(state, func(t *testing.T) {
			setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/statuses") {
					fmt.Fprintf(w, `{"values":[{"state":%q}],"next":"https://untrusted.example.invalid/page2"}`, state)
					return
				}
				fmt.Fprint(w, `{"values":[]}`)
			})
			got := statusBBHeadChecks("example/widget", checkHeadB, true)
			want := "UNKNOWN"
			if state == "FAILED" {
				want = "FAILURE"
			}
			if got.Complete || got.Result != want {
				t.Fatalf("partial evidence: %+v", got)
			}
		})
	}
}

func TestStatusPRChecksIgnoreOtherHeadAndUseLatestPipelinePerSelector(t *testing.T) {
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/statuses") {
			fmt.Fprint(w, `{"values":[]}`)
			return
		}
		fmt.Fprintf(w, `{"values":[
   {"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"commit":{"hash":%q},"selector":{"type":"pullrequests","pattern":"**"}}},
   {"state":{"name":"COMPLETED","result":{"name":"SUCCESSFUL"}},"target":{"commit":{"hash":%q},"selector":{"type":"pullrequests","pattern":"**"}}},
   {"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"commit":{"hash":%q},"selector":{"type":"pullrequests","pattern":"**"}}}]}`, checkHeadA, checkHeadB, checkHeadB)
	})
	got := statusBBHeadChecks("example/widget", checkHeadB, true)
	if !got.Complete || got.Result != "SUCCESS" {
		t.Fatalf("stale head or older rerun leaked: %+v", got)
	}
}

func TestStatusPRChecksRespectBudgetAndCooldown(t *testing.T) {
	hits := 0
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, `{"values":[]}`) })
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	got := statusBBHeadChecks("example/widget", checkHeadB, true)
	if hits != 1 || got.Complete || got.Result != "UNKNOWN" {
		t.Fatalf("budget bypass: %+v hits=%d", got, hits)
	}
	statusForgeCool("bitbucket.org", time.Hour)
	got = statusBBHeadChecks("example/widget", checkHeadB, true)
	if hits != 1 || got.Result != "UNKNOWN" {
		t.Fatalf("cooldown bypass: %+v hits=%d", got, hits)
	}
}

func TestStatusPRChecksRefreshOutsideUnchangedPRMetadata(t *testing.T) {
	calls := 0
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Path, "/statuses") {
			fmt.Fprint(w, `{"values":[{"state":"FAILED"}]}`)
		} else {
			fmt.Fprint(w, `{"values":[]}`)
		}
	})
	record := PRRecord{Number: "7"}
	raw := []byte(`{"id":7,"state":"OPEN","source":{"commit":{"hash":"` + checkHeadB + `"}}}`)
	list, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Fingerprint: "same", Raw: raw}}, false, time.Now())
	statusWriteForgeEntry("openprs", "bitbucket.org", "example/widget", "open", list)
	got := statusEnrichRecord(record, "example/widget", "bitbucket.org")
	if got.Checks != "FAILURE" || calls != 2 {
		t.Fatalf("PR row lost checks: %+v calls=%d", got, calls)
	}
}

func TestStatusPRPipelineSourceTargetBuildsItsBranch(t *testing.T) {
	hits := 0
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprintf(w, `{"values":[{"build_number":12,"state":{"name":"COMPLETED","result":{"name":"FAILED"}},"target":{"type":"pipeline_pullrequest_target","source":"topic","selector":{"type":"pull-requests","pattern":"**"},"commit":{"hash":%q}}}]}`, checkHeadB)
	})
	build, err := statusBBBuild("example/widget", "topic")
	if err != nil || build == nil || *build.Checks != "FAILURE" || *build.SHA != checkHeadB {
		t.Fatalf("source target dropped: %+v %v", build, err)
	}
	if branches := statusPipelinePageBranches([]byte(`{"values":[{"target":{"source":"topic","selector":{"type":"pull-requests"}}}]}`)); len(branches) != 1 || branches[0] != "topic" {
		t.Fatalf("PR source not cached: %v", branches)
	}
	// The shared page is the only read; a second pass within its age reuses it.
	build, err = statusBBBuild("example/widget", "topic")
	if err != nil || build == nil || *build.Checks != "FAILURE" || hits != 1 {
		t.Fatalf("shared page not reused: %+v hits=%d err=%v", build, hits, err)
	}
}

func TestStatusPRChecksResolveAbbreviatedHeadBeforeQueries(t *testing.T) {
	for _, resolved := range []string{checkHeadB, checkHeadA, "not-a-commit"} {
		t.Run(resolved, func(t *testing.T) {
			hits := 0
			setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) {
				hits++
				if strings.HasSuffix(r.URL.Path, "/commit/"+checkHeadB[:12]) {
					fmt.Fprintf(w, `{"hash":%q}`, resolved)
					return
				}
				if strings.Contains(r.URL.Path, "/statuses") {
					if !strings.Contains(r.URL.Path, checkHeadB) {
						t.Error("statuses used an abbreviated head")
					}
					fmt.Fprint(w, `{"values":[{"state":"FAILED"}]}`)
					return
				}
				if r.URL.Query().Get("target.commit.hash") != checkHeadB {
					t.Error("pipeline filter used an abbreviated head")
				}
				fmt.Fprint(w, `{"values":[]}`)
			})
			got := statusBBHeadChecks("example/widget", checkHeadB[:12], false)
			if resolved == checkHeadB {
				if got.Result != "FAILURE" || got.Head != checkHeadB || !got.Complete || hits != 3 {
					t.Fatalf("resolved head lost: %+v hits=%d", got, hits)
				}
			} else if got.Result != "UNKNOWN" || got.Complete || hits != 1 {
				t.Fatalf("unresolved head attached: %+v hits=%d", got, hits)
			}
		})
	}
}

func TestStatusPRChecksPreserveDraftLabelWithoutChecks(t *testing.T) {
	setupHeadCheckServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"values":[]}`) })
	record := PRRecord{Number: "7"}
	raw := []byte(`{"id":7,"state":"OPEN","draft":true,"source":{"commit":{"hash":"` + checkHeadB + `"}}}`)
	list, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Fingerprint: "draft", Raw: raw}}, false, time.Now())
	statusWriteForgeEntry("openprs", "bitbucket.org", "example/widget", "open", list)
	got := statusEnrichRecord(record, "example/widget", "bitbucket.org")
	if got.Checks != "draft" || got.ChecksUnsettled {
		t.Fatalf("draft label changed: %+v", got)
	}
}
