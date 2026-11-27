package wtc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixture445 is a synthetic collection with one enlisted pull request whose
// branch is checked out. Provider commands and the Bitbucket list endpoint
// are local stand-ins that count every request.
type fixture445 struct {
	c        *Context
	forge    string
	slug     string
	worktree string
	owner    string
	head     string
	calls    string
	listHits *atomic.Int32
}

const fixture445MergedAt = "2026-11-18T09:00:00Z"

// newFixture445 checks out branch topic with a gone upstream. When landed is
// true, origin/main already contains the branch head (a fast-forward merge
// fetched locally). state is what the provider reports for PR #7.
func newFixture445(t *testing.T, forge, state string, landed bool) *fixture445 {
	t.Helper()
	c := newWorkspaceFixture(t)
	t.Setenv("TMPDIR", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, ".config", "gh"))
	t.Setenv("GH_TOKEN", "synthetic-445")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("BB_API_USER", "synthetic-user")
	t.Setenv("BB_API_PASS", "synthetic-445")
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "")
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
	t.Setenv("WTC_STATUS_RETURN_MERGED", "")
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	worktree := filepath.Join(c.Collection, "widget")
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "-q", "-b", "topic", worktree, "origin/main")
	fixtureFile(t, filepath.Join(worktree, "feature.txt"), "synthetic change\n", 0644)
	fixtureGit(t, "-C", worktree, "add", "feature.txt")
	fixtureGit(t, "-C", worktree, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "feature")
	head := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD")
	// The remote branch was deleted after the merge: tracking reports [gone].
	fixtureGit(t, "-C", worktree, "config", "branch.topic.remote", "origin")
	fixtureGit(t, "-C", worktree, "config", "branch.topic.merge", "refs/heads/topic")
	if landed {
		fixtureGit(t, "--git-dir="+owner, "update-ref", "refs/remotes/origin/main", head)
	}
	remote := map[string]string{"github.com": "https://github.com/example/widget.git", "bitbucket.org": "https://bitbucket.org/example/widget.git"}[forge]
	raw, err := os.ReadFile(c.registryPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "  - name: widget") && i+1 < len(lines) {
			lines[i+1] = "    remote: " + remote
		}
	}
	fixtureFile(t, c.registryPath(), strings.Join(lines, "\n"), 0644)
	fixtureFile(t, filepath.Join(c.Collection, ".wtc-prs"), "widget 7 topic\n", 0644)
	c, err = OpenCollection(c.Collection)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("STATUS_445_CALLS", calls)
	gh := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$STATUS_445_CALLS"
case "$1 $2" in
"pr view") printf '%%s' '{"number":7,"state":%q,"title":"Synthetic change","isDraft":false,"mergedAt":"%s","closedAt":"%s","mergeCommit":{"oid":"%s"},"headRefOid":"%s","baseRefName":"main","statusCheckRollup":[]}' ;;
"pr list") printf '[]' ;;
*) printf '{}' ;;
esac
`, state, fixture445MergedAt, fixture445MergedAt, head, head)
	bb := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$STATUS_445_CALLS"
case "$1 $2" in
"pr view") printf '%%s' '{"id":7,"state":%q,"title":"Synthetic change","updated_on":"2026-10-01T09:00:00Z","merge_commit":{"hash":"%s"},"source":{"commit":{"hash":"%s"}},"destination":{"branch":{"name":"main"}}}' ;;
"pr activity") printf '%%s' '{"activities":[{"update":{"state":"MERGED","date":"%s"}}]}' ;;
*) printf '{}' ;;
esac
`, state, head, head, fixture445MergedAt)
	fixtureFile(t, filepath.Join(bin, "gh"), gh, 0700)
	fixtureFile(t, filepath.Join(bin, "bb"), bb, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	hits := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"values":[]}`))
	}))
	t.Cleanup(server.Close)
	previous := statusBBAPI
	statusBBAPI = server.URL
	t.Cleanup(func() { statusBBAPI = previous })
	// Settle Git's stat cache before any baseline is taken.
	fixtureGit(t, "-C", worktree, "status", "--porcelain")
	return &fixture445{c: c, forge: forge, slug: "example/widget", worktree: worktree, owner: owner, head: head, calls: calls, listHits: hits}
}

func (f *fixture445) providerCalls() []string {
	raw, _ := os.ReadFile(f.calls)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (f *fixture445) detailCalls() int {
	count := 0
	for _, call := range f.providerCalls() {
		if strings.HasPrefix(call, "pr view") {
			count++
		}
	}
	return count
}

// fixture445State fingerprints every file in the collection and the shared
// Git owner (refs, reflogs, index, config, objects) plus the checkout itself.
func fixture445State(t *testing.T, f *fixture445) string {
	t.Helper()
	var entries []string
	for _, root := range []string{f.c.Collection, f.owner} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			entry := root + ":" + rel + ":" + info.Mode().String()
			if info.Mode().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(data)
				entry += ":" + hex.EncodeToString(sum[:])
			} else if info.Mode()&os.ModeSymlink != 0 {
				target, _ := os.Readlink(path)
				entry += ":" + target
			}
			entries = append(entries, entry)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(entries)
	entries = append(entries, "HEAD="+fixtureGit(t, "-C", f.worktree, "symbolic-ref", "-q", "HEAD"))
	return strings.Join(entries, "\n")
}

func fixture445Diff(before, after string) string {
	left, right := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(before, "\n") {
		left[line] = true
	}
	for _, line := range strings.Split(after, "\n") {
		right[line] = true
	}
	var out []string
	for line := range left {
		if !right[line] {
			out = append(out, "- "+line)
		}
	}
	for line := range right {
		if !left[line] {
			out = append(out, "+ "+line)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// Explicit previews must not move a worktree, rewrite the PR registry, or
// publish a snapshot, even when the provider confirms a merge that would make a
// full collector return the checkout.
func TestStatus445PreviewsLeaveGitRegistryAndSnapshotsUnchanged(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture445(t, forge, "MERGED", true)
			before := fixture445State(t, f)
			snapshot, _, err := f.c.StatusLivePreviewWithFetchAge(true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.PRs) != 1 || snapshot.PRs[0].State != "MERGED" {
				t.Fatalf("preview did not observe the merge: %+v", snapshot.PRs)
			}
			if diff := fixture445Diff(before, fixture445State(t, f)); diff != "" {
				t.Fatalf("--forge preview changed collection or Git state:\n%s", diff)
			}
			if _, err := f.c.StatusLocalSnapshot(false); err != nil {
				t.Fatal(err)
			}
			if diff := fixture445Diff(before, fixture445State(t, f)); diff != "" {
				t.Fatalf("--local preview changed collection or Git state:\n%s", diff)
			}
			if _, err := f.c.ReadStatusSnapshot(); !os.IsNotExist(err) {
				t.Fatalf("--cached found a snapshot no preview should have written: %v", err)
			}
			// --cached without a snapshot falls back to the legacy text.
			if _, err := f.c.LegacyStatusText(); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if diff := fixture445Diff(before, fixture445State(t, f)); diff != "" {
				t.Fatalf("--cached changed collection or Git state:\n%s", diff)
			}
			// With a completed snapshot from a full collector (retaining the
			// branch), --cached reads it and its age without changing anything.
			t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
			if _, _, err := f.c.StatusLiveSnapshotWithFetchAge(false, true, time.Hour); err != nil {
				t.Fatal(err)
			}
			collected := fixture445State(t, f)
			if snapshot, err := f.c.ReadStatusSnapshot(); err != nil || len(snapshot.PRs) != 1 {
				t.Fatalf("cached snapshot: %+v %v", snapshot, err)
			}
			if _, err := f.c.CachedStatusAge(); err != nil {
				t.Fatal(err)
			}
			if diff := fixture445Diff(collected, fixture445State(t, f)); diff != "" {
				t.Fatalf("--cached changed collection or Git state:\n%s", diff)
			}
		})
	}
}

// The checkout moves only when a full collector (the running view) acts on the
// confirmed merge under its own eligibility gates. A preview followed by a
// collector pass is distinguishable from the preview acting alone.
func TestStatus445CollectorNotPreviewReturnsMergedCheckout(t *testing.T) {
	f := newFixture445(t, "github.com", "MERGED", true)
	if _, _, err := f.c.StatusLivePreviewWithFetchAge(true, time.Hour); err != nil {
		t.Fatal(err)
	}
	if branch := fixtureGit(t, "-C", f.worktree, "symbolic-ref", "--short", "HEAD"); branch != "topic" {
		t.Fatalf("preview moved checkout to %s", branch)
	}
	if _, _, err := f.c.StatusLiveSnapshotWithFetchAge(false, true, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := statusGit(f.worktree, "symbolic-ref", "-q", "HEAD"); err == nil {
		t.Fatal("collector did not return the landed, clean, merged checkout")
	}
	data, _ := os.ReadFile(filepath.Join(f.c.Collection, ".wtc-prs"))
	if !strings.Contains(string(data), fixture445MergedAt) {
		t.Fatalf("collector did not record the merge: %q", data)
	}
}

// A current open list is trusted: with an allowance available, the live list
// read omits the merged PR, which is then read on its own once.
func TestStatus445GoneBranchTrustsCurrentList(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture445(t, forge, "MERGED", true)
			t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
			snapshot, err := f.c.StatusForgePreview()
			if err != nil {
				t.Fatal(err)
			}
			if row := snapshot.PRs[0]; row.State != "MERGED" || f.detailCalls() != 1 {
				t.Fatalf("%+v calls=%v", row, f.providerCalls())
			}
		})
	}
}

// With the provider unavailable, local ancestry and a gone upstream are
// reported as evidence only. They never produce MERGED, a return, or a
// recorded merge.
func TestStatus445LocalEvidenceIsNotForgeMerge(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture445(t, forge, "MERGED", true)
			old := time.Now().Add(-90 * time.Minute)
			raw := []byte(`{"id":7,"number":7,"state":"OPEN","title":"Synthetic change","headRefOid":"` + f.head + `","source":{"commit":{"hash":"` + f.head + `"}}}`)
			ctx := statusAuthOperation(context.Background())
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, f.slug, "7", raw, old, old)
			until, _ := statusForgeCoolContext(ctx, forge, time.Hour)
			before := fixture445State(t, f)
			snapshot, _, err := f.c.StatusLiveSnapshotWithFetchAge(false, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			row := snapshot.PRs[0]
			if row.State != "OPEN" || row.Freshness != "deferred" || row.ObservationReason != "provider-cooldown" || row.RetryAt != until.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("cooling read misreported: %+v", row)
			}
			if got := row.LocalMergeEvidence; got != "upstream-gone; head in origin/main" {
				t.Fatalf("local evidence missing: %q", got)
			}
			if branch := fixtureGit(t, "-C", f.worktree, "symbolic-ref", "--short", "HEAD"); branch != "topic" {
				t.Fatal("local evidence returned the checkout")
			}
			data, _ := os.ReadFile(filepath.Join(f.c.Collection, ".wtc-prs"))
			if string(data) != "widget 7 topic\n" {
				t.Fatalf("local evidence recorded a merge: %q", data)
			}
			if len(f.providerCalls()) != 0 {
				t.Fatalf("cooling dispatched provider reads: %v", f.providerCalls())
			}
			for _, repo := range snapshot.Repos {
				if repo.Repo == "widget" && (repo.Next != nil || repo.Choices != nil) && !repo.PRUnconfirmed {
					t.Fatalf("local evidence granted actions: %+v", repo)
				}
			}
			if diff := fixture445Diff(before, fixture445State(t, f)); strings.Contains(diff, "widget.git:") || strings.Contains(diff, ".wtc-prs") {
				t.Fatalf("local evidence changed Git or the registry:\n%s", diff)
			}
		})
	}
}

// The newest observation of a state wins, whatever its source. A later list
// confirmation of the same open state dates a deferred fallback.
func TestStatus445NewerListObservationOutlivesOlderDetail(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture445(t, forge, "OPEN", false)
			ctx := statusAuthOperation(context.Background())
			detailAt := time.Now().Add(-150 * time.Minute)
			listAt := time.Now().Add(-90 * time.Minute)
			raw := []byte(`{"id":7,"number":7,"state":"OPEN","title":"Synthetic change"}`)
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, f.slug, "7", raw, detailAt, detailAt)
			statusWriteForgeEntryObservedContext(ctx, "stamp", forge, f.slug, "7", []byte("same"), detailAt)
			listed := statusEnrichListedContext(ctx, PRRecord{Number: "7"}, f.slug, forge, statusOpenPR{Number: "7", Raw: raw, Fingerprint: "same", Started: listAt}, false)
			if listed.StateSource != "open-list" {
				t.Fatalf("list did not date matching state: %+v", listed)
			}
			// A by-number read is then refused by the shared budget.
			t.Setenv("WTC_FORGE_MIN_INTERVAL", "3600")
			foreground424Budget(t, forge, 0, time.Now())
			got := statusReadUnlistedContext(statusAuthOperation(context.Background()), StatusRefreshBackground, PRRecord{Number: "7"}, f.slug, forge, time.Now())
			at, _ := time.Parse(time.RFC3339Nano, got.ObservedAt)
			if got.Freshness != "deferred" || got.StateSource != "open-list" || !at.Equal(listAt.Truncate(time.Second)) && !at.Equal(listAt) {
				t.Fatalf("older detail replaced newer list observation: freshness=%s source=%s observed=%s want≈%s", got.Freshness, got.StateSource, got.ObservedAt, listAt.UTC().Format(time.RFC3339Nano))
			}
			if reason := got.Reason; reason != "shared-budget" {
				t.Fatalf("deferral reason unclassified: %q", reason)
			}
		})
	}
}

// A provider observation that says DECLINED keeps archive treatment by its
// closure time when the next read is deferred.
func TestStatus445DeferredDeclineKeepsArchive(t *testing.T) {
	f := newFixture445(t, "bitbucket.org", "DECLINED", false)
	fixtureFile(t, filepath.Join(f.c.Collection, ".wtc-prs"), "widget 7 other-branch\n", 0644)
	ctx := statusAuthOperation(context.Background())
	old := time.Now().Add(-3 * time.Hour)
	raw := []byte(`{"id":7,"state":"DECLINED","title":"Synthetic change","updated_on":"2026-10-01T09:00:00Z"}`)
	statusWriteForgeEntrySinceObservedContext(ctx, "pr", "bitbucket.org", f.slug, "7", raw, old, old)
	statusForgeCoolContext(ctx, "bitbucket.org", time.Hour)
	snapshot, err := f.c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.PRs[0]
	if row.State != "DECLINED" || row.Freshness != "deferred" || !row.Archived {
		t.Fatalf("deferred decline lost archive treatment: %+v", row)
	}
	if row.ObservationReason != "provider-cooldown" {
		t.Fatalf("deferral reason: %q", row.ObservationReason)
	}
}

// A Bitbucket merge observed by an explicit refresh records its merge time and
// registry line.
func TestStatus445BitbucketForegroundMergeRecordsTime(t *testing.T) {
	f := newFixture445(t, "bitbucket.org", "MERGED", false)
	t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
	snapshot, err := f.c.StatusForegroundSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.PRs[0]
	if row.State != "MERGED" || row.MergedOn == nil || *row.MergedOn != fixture445MergedAt {
		t.Fatalf("merge time missing: %+v calls=%v", row, f.providerCalls())
	}
	data, _ := os.ReadFile(filepath.Join(f.c.Collection, ".wtc-prs"))
	if !strings.Contains(string(data), fixture445MergedAt) {
		t.Fatalf("merge not recorded: %q", data)
	}
}

func listed445Env(t *testing.T, token string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "gh"))
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("BB_API_USER", "synthetic-user")
	t.Setenv("BB_API_PASS", token)
	t.Setenv("WTC_FORGE_CACHE_AGE", "")
}

// A list confirmation moves only the state observation time. It never
// improves freshness, so old checks/reviews keep their age and actions stay
// blocked; mismatches, future times, terminal records, other credential
// contexts, cancellation, newer writes by released clients and malformed
// metadata are all ignored.
func TestStatus445ListedObservationNegativeControls(t *testing.T) {
	const headA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const headB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	open := func(head string) []byte {
		return []byte(`{"id":7,"number":7,"state":"OPEN","title":"Synthetic","headRefOid":"` + head + `","source":{"commit":{"hash":"` + head + `"}},"statusCheckRollup":[{"conclusion":"FAILURE"}]}`)
	}
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		slug, record := "example/widget", PRRecord{Number: "7"}
		read := func(ctx context.Context) statusPRDetail {
			t.Helper()
			detail, ok := statusReadObservedDetailKindContext(ctx, "pr", record, slug, forge, 365*24*time.Hour)
			if !ok {
				t.Fatal("detail unavailable")
			}
			return detail
		}
		at := func(detail statusPRDetail) time.Time {
			parsed, _ := time.Parse(time.RFC3339Nano, detail.ObservedAt)
			return parsed
		}
		setup := func(t *testing.T, detailAge time.Duration, head string) context.Context {
			t.Setenv("TMPDIR", t.TempDir())
			listed445Env(t, "synthetic-a")
			ctx := statusAuthOperation(context.Background())
			when := time.Now().Add(-detailAge)
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, slug, "7", open(head), when, when)
			return ctx
		}
		listed := func(ctx context.Context, head string, age time.Duration) time.Time {
			when := time.Now().Add(-age)
			statusRecordListedContext(ctx, forge, slug, statusPRDetail{Number: "7", State: "OPEN", HeadCommit: head}, when)
			return when
		}
		t.Run(forge+"/same-state-moves-state-time-only", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			when := listed(ctx, headA, 90*time.Minute)
			got := read(ctx)
			if !at(got).Equal(when) || got.StateSource != "open-list" || got.Freshness != "stale" || !statusObservationUnavailable(got.Freshness) {
				t.Fatalf("%+v", got)
			}
			if forge == "github.com" && got.Checks != "FAILURE" {
				t.Fatalf("checks changed: %+v", got)
			}
		})
		t.Run(forge+"/recent-list-cannot-make-stale-detail-current", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			listed(ctx, headA, 10*time.Second)
			if got := read(ctx); got.Freshness != "stale" {
				t.Fatalf("list confirmation renewed detail freshness: %+v", got)
			}
		})
		t.Run(forge+"/different-head", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			listed(ctx, headB, 90*time.Minute)
			if got := read(ctx); got.StateSource != "detail-cache" {
				t.Fatalf("different head redated: %+v", got)
			}
		})
		t.Run(forge+"/absent-list-head-is-state-only", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			when := listed(ctx, "", 90*time.Minute)
			got := read(ctx)
			if !at(got).Equal(when) || got.HeadCommit != headA || got.Freshness != "stale" {
				t.Fatalf("%+v", got)
			}
		})
		t.Run(forge+"/older-list-does-not-regress", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			when := listed(ctx, headA, 90*time.Minute)
			listed(ctx, headA, 120*time.Minute)
			if got := read(ctx); !at(got).Equal(when) {
				t.Fatalf("older confirmation replaced newer: %+v", got)
			}
		})
		t.Run(forge+"/future-time", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			listed(ctx, headA, -time.Hour)
			if _, _, ok := statusReadForgeEntryObservedContext(ctx, "listed", forge, slug, "7", 365*24*time.Hour); ok {
				t.Fatal("future confirmation stored")
			}
			statusWriteForgeEntryObservedContext(ctx, "listed", forge, slug, "7", []byte("OPEN\x00"+headA), time.Now().Add(time.Hour))
			if got := read(ctx); got.StateSource != "detail-cache" {
				t.Fatalf("future-dated metadata redated: %+v", got)
			}
		})
		t.Run(forge+"/terminal-record", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			merged := time.Now().Add(-2 * time.Hour)
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, slug, "7", []byte(`{"id":7,"number":7,"state":"MERGED","title":"Synthetic"}`), merged, merged)
			listed(ctx, headA, time.Minute)
			got := read(ctx)
			if got.State != "MERGED" || got.StateSource == "open-list" {
				t.Fatalf("open confirmation touched terminal record: %+v", got)
			}
			statusRecordListedContext(ctx, forge, slug, statusPRDetail{Number: "8", State: "MERGED"}, time.Now())
			if _, _, ok := statusReadForgeEntryObservedContext(ctx, "listed", forge, slug, "8", 365*24*time.Hour); ok {
				t.Fatal("terminal state stored as list confirmation")
			}
		})
		t.Run(forge+"/auth-switch", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			listed(ctx, headA, 90*time.Minute)
			listed445Env(t, "synthetic-b")
			other := statusAuthOperation(context.Background())
			if _, _, ok := statusReadForgeEntryObservedContext(other, "listed", forge, slug, "7", 365*24*time.Hour); ok {
				t.Fatal("confirmation crossed credential contexts")
			}
		})
		t.Run(forge+"/cancelled", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			listed(cancelled, headA, 90*time.Minute)
			if _, _, ok := statusReadForgeEntryObservedContext(ctx, "listed", forge, slug, "7", 365*24*time.Hour); ok {
				t.Fatal("cancelled confirmation stored")
			}
		})
		t.Run(forge+"/newer-released-client-write-wins", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			listed(ctx, headA, 90*time.Minute)
			// Released clients publish detail through the same entry and never
			// read or write list confirmations.
			newer := time.Now().Add(-30 * time.Minute)
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", forge, slug, "7", open(headA), newer, newer)
			if got := read(ctx); !at(got).Equal(newer) || got.StateSource != "detail-cache" {
				t.Fatalf("older confirmation replaced newer detail: %+v", got)
			}
		})
		t.Run(forge+"/malformed-metadata", func(t *testing.T) {
			ctx := setup(t, 150*time.Minute, headA)
			for _, value := range []string{"OPEN\x00" + strings.Repeat("a", 65), "OPEN\x00ZZZ", "MERGED\x00", strings.Repeat("x", 4096)} {
				statusWriteForgeEntryObservedContext(ctx, "listed", forge, slug, "7", []byte(value), time.Now().Add(-time.Minute))
				if got := read(ctx); got.StateSource != "detail-cache" {
					t.Fatalf("malformed metadata %q redated: %+v", value, got)
				}
			}
		})
	}
}

// A by-number read whose cache lock cannot be taken names the lock; one that
// waits past its deadline names the deadline. Neither dispatches a request.
func TestStatus445LockRefusalsAreClassified(t *testing.T) {
	f := newFixture445(t, "github.com", "OPEN", false)
	ctx := statusAuthOperation(context.Background())
	old := time.Now().Add(-time.Hour)
	statusWriteForgeEntrySinceObservedContext(ctx, "pr", "github.com", f.slug, "7", []byte(`{"number":7,"state":"OPEN","title":"Synthetic"}`), old, old)
	path := statusForgeEntryPathContext(ctx, "pr", "github.com", f.slug, "7")
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	got := statusReadUnlistedContext(ctx, StatusRefreshBackground, PRRecord{Number: "7"}, f.slug, "github.com", time.Now())
	if got.Freshness != "deferred" || got.Reason != "cache-lock" {
		t.Fatalf("unusable lock: %+v", got)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	unlock, err := statusLockCacheContext(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	got = statusReadUnlistedContext(short, StatusRefreshBackground, PRRecord{Number: "7"}, f.slug, "github.com", time.Now())
	if got.Freshness != "deferred" || got.Reason != "deadline" {
		t.Fatalf("held lock: %+v", got)
	}
	if len(f.providerCalls()) != 0 {
		t.Fatalf("refused reads dispatched: %v", f.providerCalls())
	}
}

// A PR that stays open after its branch was deleted, missing from the open
// list, is read once per list, not on every pass.
func TestStatus445GoneBranchReadIsBoundedPerInterval(t *testing.T) {
	for _, forge := range []string{"github.com", "bitbucket.org"} {
		t.Run(forge, func(t *testing.T) {
			f := newFixture445(t, forge, "OPEN", false)
			for pass := 0; pass < 3; pass++ {
				snapshot, err := f.c.StatusForgePreview()
				if err != nil {
					t.Fatal(err)
				}
				if row := snapshot.PRs[0]; row.State != "OPEN" || row.LocalMergeEvidence != "upstream-gone" {
					t.Fatalf("pass %d: %+v", pass, row)
				}
			}
			if f.detailCalls() != 1 {
				t.Fatalf("detail reads across three passes: %v", f.providerCalls())
			}
		})
	}
}

// A GitHub close can be reopened, so a deferred CLOSED observation keeps its
// row active rather than archiving it.
func TestStatus445DeferredGitHubCloseStaysActive(t *testing.T) {
	f := newFixture445(t, "github.com", "CLOSED", false)
	fixtureFile(t, filepath.Join(f.c.Collection, ".wtc-prs"), "widget 7 other-branch\n", 0644)
	ctx := statusAuthOperation(context.Background())
	old := time.Now().Add(-3 * time.Hour)
	statusWriteForgeEntrySinceObservedContext(ctx, "pr", "github.com", f.slug, "7", []byte(`{"number":7,"state":"CLOSED","title":"Synthetic","closedAt":"2026-10-01T09:00:00Z"}`), old, old)
	statusForgeCoolContext(ctx, "github.com", time.Hour)
	snapshot, err := f.c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	if row := snapshot.PRs[0]; row.State != "CLOSED" || row.Freshness != "deferred" || row.Archived {
		t.Fatalf("deferred close archived: %+v", row)
	}
}

// A retained Bitbucket merge observed without a merge time completes it from
// the activity log and is recorded; with the activity read unavailable the
// time stays unknown and nothing is recorded.
func TestStatus445BitbucketRetainedMergeTime(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			f := newFixture445(t, "bitbucket.org", "MERGED", false)
			t.Setenv("WTC_STATUS_RETURN_MERGED", "off")
			fixtureFile(t, filepath.Join(f.c.Collection, ".wtc-prs"), "widget 7 other-branch\n", 0644)
			ctx := statusAuthOperation(context.Background())
			at := time.Now().Add(-10 * time.Minute)
			statusWriteForgeEntrySinceObservedContext(ctx, "pr", "bitbucket.org", f.slug, "7", []byte(`{"id":7,"state":"MERGED","title":"Synthetic"}`), at, at)
			if !available {
				statusForgeCoolContext(ctx, "bitbucket.org", time.Hour)
			}
			snapshot, _, err := f.c.StatusLiveSnapshotWithFetchAge(false, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			row := snapshot.PRs[0]
			data, _ := os.ReadFile(filepath.Join(f.c.Collection, ".wtc-prs"))
			if available {
				if row.State != "MERGED" || row.MergedOn == nil || *row.MergedOn != fixture445MergedAt || !strings.Contains(string(data), fixture445MergedAt) {
					t.Fatalf("retained merge time not completed: %+v %q", row, data)
				}
				return
			}
			if row.State != "MERGED" || row.MergedOn != nil || string(data) != "widget 7 other-branch\n" || len(f.providerCalls()) != 0 {
				t.Fatalf("unavailable activity invented a time: %+v %q %v", row, data, f.providerCalls())
			}
		})
	}
}

// An earlier DECLINED observation of a PR that a current open list omits is
// final: it is not read again and keeps archive treatment by its closure time.
func TestStatus445UnavailableDeclineKeepsArchive(t *testing.T) {
	f := newFixture445(t, "bitbucket.org", "DECLINED", false)
	fixtureFile(t, filepath.Join(f.c.Collection, ".wtc-prs"), "widget 7 other-branch\n", 0644)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$STATUS_445_CALLS\"\nexit 1\n", 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := statusAuthOperation(context.Background())
	old := time.Now().Add(-3 * time.Hour)
	statusWriteForgeEntrySinceObservedContext(ctx, "pr", "bitbucket.org", f.slug, "7", []byte(`{"id":7,"state":"DECLINED","title":"Synthetic change","updated_on":"2026-10-01T09:00:00Z"}`), old, old)
	snapshot, err := f.c.StatusForgePreview()
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.PRs[0]
	if row.State != "DECLINED" || statusObservationUnavailable(row.Freshness) || !row.Archived || f.detailCalls() != 0 {
		t.Fatalf("final decline: %+v calls=%v", row, f.providerCalls())
	}
}
