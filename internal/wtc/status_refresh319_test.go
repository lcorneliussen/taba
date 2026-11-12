package wtc

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStatus319RetainedMergeRejectsActiveWriters(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		for _, writer := range []string{"list", "view", "full-audit", "shared-fetch"} {
			t.Run(forge+"/"+writer, func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
				t.Setenv("WTC_FORGE_FULL_REFRESH", "60")
				record, slug := PRRecord{Number: "7"}, "example/widget"
				merged := []byte(`{"number":7,"id":7,"state":"MERGED"}`)
				active := []byte(`{"number":7,"id":7,"state":"OPEN"}`)
				statusWriteForgeEntrySince("pr", forge, slug, "7", merged, time.Now().Add(-time.Hour))
				past := time.Now().Add(-time.Hour)
				if err := os.Chtimes(statusForgeCachePath(forge, slug, "7"), past, past); err != nil {
					t.Fatal(err)
				}
				before, _ := statusReadObservedDetail(record, slug, forge, 24*time.Hour)
				bin := t.TempDir()
				for _, command := range []string{"gh", "bb"} {
					fixtureFile(t, bin+"/"+command, "#!/bin/sh\nprintf '%s' '{\"number\":7,\"id\":7,\"state\":\"OPEN\"}'\n", 0700)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				var got statusPRDetail
				switch writer {
				case "list":
					got, _, _ = statusDetailFromOpenItem(record, slug, forge, statusOpenPR{Number: "7", Fingerprint: "changed", Raw: active, Started: time.Now()})
				case "view":
					got = statusViewPRWithLive(record, slug, forge, true, true, nil)
				case "full-audit":
					got = statusConfirmFullPR(record, slug, forge, before, "merged-cache")
				case "shared-fetch":
					raw, live, err := statusFetchShared("pr", forge, slug, "7", func() ([]byte, error) { return active, nil })
					if err != nil || live {
						t.Fatalf("losing fetch: live=%v err=%v", live, err)
					}
					got, _ = statusParseForgeDetail(forge, raw, record)
				}
				if got.State != "MERGED" || got.MergedOn != "" {
					t.Fatalf("confirmed merge replaced or dated: %+v", got)
				}
				if writer != "shared-fetch" && got.ObservedAt != before.ObservedAt {
					t.Fatalf("merge observation redated: before=%+v after=%+v", before, got)
				}
				if writer == "view" || writer == "full-audit" {
					if got.Freshness != "unavailable" {
						t.Fatalf("conflicting response claimed confirmed: %+v", got)
					}
				}
				if saved, _ := statusReadForgeEntryAny("pr", forge, slug, "7"); string(saved) != string(merged) {
					t.Fatalf("merge cache replaced: %s", saved)
				}
				for _, kind := range []string{"omit", "full"} {
					if _, ok := statusReadForgeEntryAny(kind, forge, slug, "7"); ok {
						t.Fatalf("losing response published %s marker", kind)
					}
				}
			})
		}
	}
}

func TestStatus319FirstMergeWinsAcrossNewerActiveResponse(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	started := time.Now()
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		active := []byte(`{"number":7,"id":7,"state":"OPEN"}`)
		merged := []byte(`{"number":7,"id":7,"state":"MERGED"}`)
		statusWriteForgeEntrySince("pr", forge, "example/widget", "7", active, started.Add(time.Second))
		if !statusWriteForgeEntrySince("pr", forge, "example/widget", "7", merged, started) {
			t.Fatal("first confirmed merge lost to active replica")
		}
		if statusWriteForgeEntrySince("pr", forge, "example/widget", "7", active, started.Add(2*time.Second)) {
			t.Fatal("active replica reopened confirmed merge")
		}
	}
}

func TestStatus319OmittedObservationIsDeferred(t *testing.T) {
	for _, draft := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "draft"}[draft], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "bitbucket.org", "example/widget"
			raw, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", raw)
			body, _ := json.Marshal(map[string]any{"id": 7, "state": "OPEN", "draft": draft})
			statusWriteForgeCache(forge, slug, "7", body)
			past := time.Now().Add(-3 * time.Minute)
			if err := os.Chtimes(statusForgeCachePath(forge, slug, "7"), past, past); err != nil {
				t.Fatal(err)
			}
			if !statusTakeForgeToken(forge) {
				t.Fatal("initial token")
			}
			got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			encoded, _ := json.Marshal(got)
			var fields map[string]any
			_ = json.Unmarshal(encoded, &fields)
			if fields["Freshness"] != "deferred" || fields["ObservedAt"] == nil || fields["ObservedAt"] == "" {
				t.Fatalf("old fact claimed current: %s", encoded)
			}
			want := "OPEN"
			if draft {
				want = "DRAFT"
			}
			if got.State != want {
				t.Fatalf("cached fact lost: %+v", got)
			}
			if _, ok := statusReadForgeEntry("omit", forge, slug, "7"); ok {
				t.Fatal("deferred confirmation marked complete")
			}
		})
	}
}

func TestStatus319CompetingPanesEventuallyConfirm(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge, slug := "bitbucket.org", "example/widget"
	raw, _ := statusEncodeOpenPRs(nil, false, time.Now())
	statusWriteForgeEntry("openprs", forge, slug, "open", raw)
	statusWriteForgeCache(forge, slug, "7", []byte(`{"id":7,"state":"OPEN","draft":true}`))
	dir := t.TempDir()
	fixtureFile(t, dir+"/bb", "#!/bin/sh\nprintf '%s' '{\"id\":7,\"state\":\"MERGED\",\"merged_on\":\"2026-11-04T10:01:00Z\"}'\n", 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for tick := 0; tick < 3; tick++ {
		// A competing pane attempts admission before confirmation on each interval.
		if err := os.WriteFile(statusForgeStatePath("budget", forge), []byte("1 "+fmt.Sprint(time.Now().UnixNano())), 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = statusSingleFlight("openprs", forge, fmt.Sprintf("example/competitor%d", tick), "open", func() ([]byte, error) { return []byte(`{"prs":[]}`), nil })
		got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
		if got.State == "MERGED" {
			return
		}
	}
	t.Fatal("omitted draft starved behind competing panes for every replenished token")
}

func status319Refill(t *testing.T, forge string) {
	t.Helper()
	_, every := statusForgeAllowance(forge)
	raw := fmt.Sprintf("0 %d\n", time.Now().Add(-every).UnixNano())
	if err := os.WriteFile(statusForgeStatePath("budget", forge), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStatus319FIFOAndAbandonedPane(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge := "bitbucket.org"
	if !statusTakeForgeToken(forge) {
		t.Fatal("initial token")
	}
	keys := []string{statusConfirmationKey("example/widget#7"), statusConfirmationKey("example/widget#8")}
	for _, key := range keys {
		if statusTakeForgeTokenFor(forge, key) {
			t.Fatal("ceiling bypass")
		}
	}
	// A retry of the first pane must keep its place without adding another reservation.
	if statusTakeForgeTokenFor(forge, keys[0]) {
		t.Fatal("spent token reused")
	}
	for _, key := range keys {
		status319Refill(t, forge)
		if statusTakeForgeToken(forge) {
			t.Fatal("ordinary pane stole reserved token")
		}
		if key == keys[0] && statusTakeForgeTokenFor(forge, keys[1]) {
			t.Fatal("later confirmation jumped queue")
		}
		if !statusTakeForgeTokenFor(forge, key) || statusTakeForgeToken(forge) {
			t.Fatal("FIFO or ceiling failed")
		}
	}
	status319Refill(t, forge)
	if !statusTakeForgeToken(forge) {
		t.Fatal("ordinary traffic did not resume")
	}
	if statusTakeForgeTokenFor(forge, keys[0]) {
		t.Fatal("spent token reused")
	}
	queue := statusReadPendingConfirmations(forge, time.Now())
	queue[0].Seen = time.Now().Add(-statusPendingLifetime - time.Second)
	statusSavePendingConfirmations(forge, queue)
	status319Refill(t, forge)
	if !statusTakeForgeToken(forge) {
		t.Fatal("abandoned pane blocked ordinary traffic")
	}
}

func TestStatus319QueueIsBoundedAndDoesNotFollowSymlinks(t *testing.T) {
	for _, mode := range []string{"corrupt", "oversize", "directory", "symlink", "too-many", "future"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge := "bitbucket.org"
			if !statusSecureCacheDir(statusForgeCacheDir()) {
				t.Fatal("cache directory")
			}
			path := statusForgeStatePath("pending", forge)
			victim := t.TempDir() + "/victim"
			fixtureFile(t, victim, "unchanged", 0600)
			switch mode {
			case "corrupt":
				fixtureFile(t, path, "{bad", 0600)
			case "oversize":
				fixtureFile(t, path, strings.Repeat(" ", 65537), 0600)
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(victim, path); err != nil {
					t.Fatal(err)
				}
			case "too-many":
				queue := make([]statusPendingConfirmation, statusPendingLimit+1)
				for i := range queue {
					queue[i] = statusPendingConfirmation{Key: statusConfirmationKey(fmt.Sprint(i)), Seen: time.Now()}
				}
				statusSavePendingConfirmations(forge, queue)
			case "future":
				statusSavePendingConfirmations(forge, []statusPendingConfirmation{{Key: statusConfirmationKey("future"), Seen: time.Now().Add(time.Hour)}})
			}
			if !statusTakeForgeToken(forge) {
				t.Fatal("invalid queue blocked ordinary request")
			}
			raw, err := os.ReadFile(victim)
			if err != nil || string(raw) != "unchanged" {
				t.Fatal("queue write followed symlink")
			}
			if mode != "directory" {
				info, err := os.Lstat(path)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("queue is not a private regular file")
				}
			}
		})
	}
}

func TestStatus319ConfirmationFailuresPreserveFacts(t *testing.T) {
	for _, mode := range []string{"error", "malformed", "identity", "cooling", "rate-limit"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "bitbucket.org", "example/widget"
			list, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", list)
			original := []byte(`{"id":7,"state":"OPEN","draft":true}`)
			statusWriteForgeCache(forge, slug, "7", original)
			dir := t.TempDir()
			script := "#!/bin/sh\n"
			switch mode {
			case "error":
				script += "exit 1\n"
			case "malformed":
				script += "printf 'invalid'\n"
			case "identity":
				script += "printf '%s' '{\"id\":8,\"state\":\"MERGED\"}'\n"
			case "rate-limit":
				script += "printf 'HTTP 429 Retry-After: 30' >&2\nexit 1\n"
			case "cooling":
				statusForgeCool(forge, time.Minute)
				script += "exit 99\n"
			}
			fixtureFile(t, dir+"/bb", script, 0700)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			wantFreshness := "unavailable"
			if mode == "cooling" || mode == "rate-limit" {
				wantFreshness = "deferred"
			}
			if got.State != "DRAFT" || got.Freshness != wantFreshness {
				t.Fatalf("failed read claimed current/merged or lost cached state: %+v", got)
			}
			saved, _ := statusReadForgeEntryAny("pr", forge, slug, "7")
			if string(saved) != string(original) {
				t.Fatal("bad response replaced cached fact")
			}
			if _, ok := statusReadForgeEntry("omit", forge, slug, "7"); ok {
				t.Fatal("failure marked confirmed")
			}
			if len(statusReadPendingConfirmations(forge, time.Now())) != 0 {
				t.Fatal("completed request retained reservation")
			}
		})
	}
}

func TestStatus319StaleFactsCannotProposeNextAction(t *testing.T) {
	for _, freshness := range []string{"stale", "deferred"} {
		snapshot := StatusSnapshot{Repos: []StatusRepo{{Repo: "widget", Dir: "widget", BranchKind: "branch", Branch: "topic", Ahead: 1, Tree: "clean", PR: &StatusPRFacts{Number: "7", Freshness: freshness, Checks: "SUCCESS", Merge: "CLEAN", Review: "approved"}}}}
		statusNext(&snapshot)
		if snapshot.Repos[0].Next != nil || snapshot.Repos[0].Choices != nil {
			t.Fatalf("%s facts proposed an action", freshness)
		}
		snapshot.PRs = []StatusPRRow{{Repo: "widget", Number: "7", State: "DRAFT", Draft: true, Freshness: freshness, ObservedAt: "2026-11-04T10:01:00Z"}}
		raw, _ := json.Marshal(snapshot)
		if !strings.Contains(string(raw), `"freshness":"`+freshness+`"`) || !strings.Contains(snapshot.Markdown(), freshness+" forge observation; observed 2026-11-04T10:01:00Z") {
			t.Fatal("snapshot freshness was lost")
		}
	}
}

func TestStatus319FreshUnchangedListKeepsStateCurrent(t *testing.T) {
	for _, forge := range []string{"bitbucket.org", "github.com"} {
		for _, draft := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/draft=%t", forge, draft), func(t *testing.T) {
				t.Setenv("TMPDIR", t.TempDir())
				t.Setenv("WTC_FORGE_FULL_REFRESH", "600")
				slug := "example/widget"
				record := PRRecord{Number: "7"}
				body := []byte(fmt.Sprintf(`{"id":7,"state":"OPEN","draft":%t,"title":"Full"}`, draft))
				if forge == "github.com" {
					body = []byte(fmt.Sprintf(`{"number":7,"state":"OPEN","isDraft":%t,"title":"Full","statusCheckRollup":[{"conclusion":"SUCCESS","status":"COMPLETED"}]}`, draft))
				}
				statusWriteForgeCache(forge, slug, "7", body)
				past := time.Now().Add(-3 * time.Minute)
				if err := os.Chtimes(statusForgeCachePath(forge, slug, "7"), past, past); err != nil {
					t.Fatal(err)
				}
				statusMarkFullRefresh(forge, slug, "7")
				statusWriteForgeEntry("stamp", forge, slug, "7", []byte("unchanged"))
				started := time.Now()
				raw, _ := statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Fingerprint: "unchanged", Raw: body}}, false, started)
				statusWriteForgeEntry("openprs", forge, slug, "open", raw)
				got := statusEnrichRecordMetadata(record, slug, forge)
				observed, err := time.Parse(time.RFC3339Nano, got.ObservedAt)
				want := "OPEN"
				if draft {
					want = "DRAFT"
				}
				if got.State != want || got.Freshness != "cached" || err != nil || !observed.Equal(started) || got.StateSource != "open-list" {
					t.Fatalf("fresh unchanged list lost its observation: %+v", got)
				}
				info, err := os.Stat(statusForgeCachePath(forge, slug, "7"))
				if err != nil || !info.ModTime().Equal(past) {
					t.Fatal("fresh list pretended full detail was fetched again")
				}
			})
		}
	}
}

func TestStatus319SettledHistoricalMergeKeepsAuthority(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			c := newWorkspaceFixture(t)
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_CACHE_AGE", "")
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "github.com", "example/widget"
			check, status := "SUCCESS", "COMPLETED"
			if pending {
				check, status = "", "IN_PROGRESS"
			}
			body := []byte(fmt.Sprintf(`{"number":7,"state":"MERGED","mergedAt":"2026-11-04T10:01:00Z","statusCheckRollup":[{"conclusion":%q,"status":%q}]}`, check, status))
			statusWriteForgeCache(forge, slug, "7", body)
			past := time.Now().Add(-3 * time.Minute)
			if err := os.Chtimes(statusForgeCachePath(forge, slug, "7"), past, past); err != nil {
				t.Fatal(err)
			}
			list, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", list)
			if !statusTakeForgeToken(forge) {
				t.Fatal("initial token")
			}
			record := PRRecord{Repo: "widget", Number: "7", Branch: "topic"}
			fixtureFile(t, c.PRFile(), "widget 7 topic - Synthetic merge\n", 0600)
			got := statusEnrichRecordMetadata(record, slug, forge)
			if got.State != "MERGED" {
				t.Fatalf("merge evidence lost: %+v", got)
			}
			if pending {
				if got.Freshness != "deferred" || !got.ChecksUnsettled {
					t.Fatalf("pending historical checks claimed settled: %+v", got)
				}
			} else if got.Freshness != "settled" || got.StateSource != "settled-cache" || got.ObservedAt == "" || statusObservationUnavailable(got.Freshness) {
				t.Fatalf("valid 24h settled cache invalidated at 90s: %+v", got)
			}
			count, err := c.recordMergedPRs([]PRRecord{record}, []statusPRDetail{got})
			want := 1
			if pending {
				want = 0
			}
			if err != nil || count != want {
				t.Fatalf("historical authority: count=%d err=%v", count, err)
			}
		})
	}
}

func TestStatus319OmittedConfirmationCoalescesAcrossPanes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge, slug := "bitbucket.org", "example/widget"
	list, _ := statusEncodeOpenPRs(nil, false, time.Now())
	statusWriteForgeEntry("openprs", forge, slug, "open", list)
	statusWriteForgeCache(forge, slug, "7", []byte(`{"id":7,"state":"OPEN","draft":true}`))
	dir := t.TempDir()
	views, started, release := dir+"/views", dir+"/started", dir+"/release"
	defer os.WriteFile(release, []byte("release"), 0600)
	t.Setenv("WTC_TEST_BB_VIEWS", views)
	t.Setenv("WTC_TEST_BB_STARTED", started)
	t.Setenv("WTC_TEST_BB_RELEASE", release)
	fixtureFile(t, dir+"/bb", `#!/bin/sh
printf 'view\n' >> "$WTC_TEST_BB_VIEWS"
touch "$WTC_TEST_BB_STARTED"
while [ ! -f "$WTC_TEST_BB_RELEASE" ]; do sleep 0.01; done
printf '%s' '{"id":7,"state":"MERGED","draft":true,"merged_on":"2026-11-04T10:01:00Z"}'
`, 0700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	begin := make(chan struct{})
	results := make(chan statusPRDetail, 4)
	for range 4 {
		go func() { <-begin; results <- statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge) }()
	}
	close(begin)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(started); err != nil {
		t.Fatal("by-number confirmation did not start")
	}
	fixtureFile(t, release, "release", 0600)
	for range 4 {
		select {
		case got := <-results:
			if got.State != "MERGED" || statusObservationUnavailable(got.Freshness) {
				t.Fatalf("stale draft overrode merged confirmation: %+v", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("confirmation did not finish")
		}
	}
	if count := statusTestFileLines(t, views); count != 1 || statusTakeForgeToken(forge) {
		t.Fatalf("coalescing/ceiling: %d reads", count)
	}
}

func TestStatus319StaleMergedFactsCannotReturnBranch(t *testing.T) {
	c, target, merge := squashFixture(t, true)
	records := []PRRecord{{Repo: "widget", Number: "7", Branch: "topic"}}
	for _, freshness := range []string{"stale", "deferred"} {
		snapshot, err := c.StatusLocalSnapshot(false)
		if err != nil {
			t.Fatal(err)
		}
		details := []statusPRDetail{{Number: "7", State: "MERGED", MergeCommit: merge, Freshness: freshness}}
		c.statusReturnMerged(&snapshot, records, details)
		if branch := fixtureGit(t, "-C", target.path, "symbolic-ref", "--short", "HEAD"); branch != "topic" {
			t.Fatalf("%s merge returned branch", freshness)
		}
		snapshot.PRs = []StatusPRRow{{Repo: "widget", Number: "7", State: "MERGED", OnBranch: true, MergeCommit: merge, Freshness: freshness}}
		statusNext(&snapshot)
		for _, row := range snapshot.Repos {
			if row.Repo == "widget" && (row.Next != nil || row.Choices != nil) {
				t.Fatalf("%s merge proposed next action", freshness)
			}
		}
	}
}

func TestStatus319ResolvedConfirmationReleasesReservation(t *testing.T) {
	for _, resolved := range []string{"listed", "settled"} {
		t.Run(resolved, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "bitbucket.org", "example/widget"
			list, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", list)
			body := []byte(`{"id":7,"state":"OPEN","draft":true}`)
			statusWriteForgeCache(forge, slug, "7", body)
			if !statusTakeForgeToken(forge) {
				t.Fatal("initial token")
			}
			if got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge); got.Freshness != "deferred" {
				t.Fatal(got)
			}
			if resolved == "listed" {
				list, _ = statusEncodeOpenPRs([]statusOpenPR{{Number: "7", Fingerprint: "current", Raw: body}}, false, time.Now())
				statusWriteForgeEntry("openprs", forge, slug, "open", list)
			} else {
				statusWriteForgeCache(forge, slug, "7", []byte(`{"id":7,"state":"MERGED","merged_on":"2026-11-04T10:01:00Z"}`))
			}
			status319Refill(t, forge)
			got := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			if statusObservationUnavailable(got.Freshness) || !statusTakeForgeToken(forge) {
				t.Fatalf("resolved %s confirmation blocked ordinary traffic: %+v", resolved, got)
			}
		})
	}
}

func TestStatus319CoolingRecoversWithFullValidResponse(t *testing.T) {
	for _, draft := range []bool{false, true} {
		t.Run(fmt.Sprint(draft), func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			forge, slug := "bitbucket.org", "example/widget"
			list, _ := statusEncodeOpenPRs(nil, false, time.Now())
			statusWriteForgeEntry("openprs", forge, slug, "open", list)
			statusWriteForgeCache(forge, slug, "7", []byte(fmt.Sprintf(`{"id":7,"state":"OPEN","draft":%t}`, draft)))
			fakeLargeBB(t, map[string]string{"view": `{"id":7,"state":"MERGED","draft":true,"merged_on":"2026-11-04T10:01:00Z"}`})
			statusForgeCool(forge, time.Minute)
			before := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			if before.Freshness != "deferred" || before.State == "MERGED" {
				t.Fatalf("cooling claimed merge: %+v", before)
			}
			// Expire only the synthetic cooldown; the next pass still gets one token.
			if err := os.WriteFile(statusForgeStatePath("cool", forge), []byte(fmt.Sprint(time.Now().Add(-time.Second).UnixNano())), 0600); err != nil {
				t.Fatal(err)
			}
			status319Refill(t, forge)
			after := statusEnrichRecordMetadata(PRRecord{Number: "7"}, slug, forge)
			if after.State != "MERGED" || after.Freshness != "live" || after.ObservedAt == "" || statusTakeForgeToken(forge) {
				t.Fatalf("recovery lost state/full response or exceeded allowance: %+v", after)
			}
		})
	}
}

func TestStatus319RetirementDoesNotClaimUnknownStateIsOpen(t *testing.T) {
	for _, represented := range []bool{false, true} {
		snapshot := StatusSnapshot{Collection: "synthetic", Retirement: []RetirementAssessment{{Collection: "synthetic"}}}
		if represented {
			snapshot.Repos = []StatusRepo{{Collection: "synthetic", Repo: "widget", Dir: "widget", PR: &StatusPRFacts{Number: "7", Freshness: "unavailable"}}}
		} else {
			snapshot.PRs = []StatusPRRow{{Repo: "widget", Number: "7", State: "UNKNOWN", Freshness: "unavailable"}}
		}
		statusRetirementFacts(&snapshot)
		reasons := strings.Join(snapshot.Retirement[0].Blockers, "\n")
		if !strings.Contains(reasons, "state is unconfirmed") || strings.Contains(reasons, "remains open") {
			t.Fatal(reasons)
		}
	}
}

func TestStatus319ReservationLeavesSpareBurstCapacity(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
	forge := "bitbucket.org"
	burst, _ := statusForgeAllowance(forge)
	for range burst {
		if !statusTakeForgeToken(forge) {
			t.Fatal("initial burst")
		}
	}
	key := statusConfirmationKey("example/widget#7")
	if statusTakeForgeTokenFor(forge, key) {
		t.Fatal("exhausted budget admitted confirmation")
	}
	// With no near refill the one-shot pane exits; restore the standard burst.
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	burst, _ = statusForgeAllowance(forge)
	// Refill the bucket while the one-shot confirmation's pane is absent.
	if err := os.WriteFile(statusForgeStatePath("budget", forge), []byte(fmt.Sprintf("%d %d", burst, time.Now().UnixNano())), 0600); err != nil {
		t.Fatal(err)
	}
	for range burst - 1 {
		if !statusTakeForgeToken(forge) {
			t.Fatal("abandoned reservation idled unreserved burst tokens")
		}
	}
	if statusTakeForgeToken(forge) {
		t.Fatal("ordinary traffic spent the reserved token")
	}
	if !statusTakeForgeTokenFor(forge, key) || statusTakeForgeToken(forge) {
		t.Fatal("reservation or ceiling was lost")
	}
}

func TestStatus319UnknownEnlistedPRBlocksActionsWithoutClaimingOpen(t *testing.T) {
	c := newWorkspaceFixture(t)
	fixtureGit(t, "-C", c.Harness, "switch", "-qc", "topic")
	if _, err := c.EnlistPR(PRRecord{Repo: "agent-harness", Number: "12", Branch: "topic", Title: "Synthetic change"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repos {
		if row.Dir != "harness" {
			continue
		}
		if row.PR != nil || !row.PRUnconfirmed || row.Next != nil || row.Choices != nil {
			t.Fatalf("unknown enlisted PR asserted open state or authorized action: %+v", row)
		}
		return
	}
	t.Fatal("harness row absent")
}
