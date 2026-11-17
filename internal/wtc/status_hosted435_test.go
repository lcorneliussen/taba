package wtc

import (
	"fmt"
	"path/filepath"
	"taba.sh/taba/internal/next"
	"testing"
	"time"
)

func hosted435Snapshots() (StatusSnapshot, StatusSnapshot) {
	url := "https://github.com/example/widget/pull/7"
	head := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Now()
	repo := StatusRepo{Collection: "fixture", Repo: "widget", Dir: "widget", Worktree: "checkout/widget", Slug: "example/widget", Forge: "github.com", BranchKind: "branch", Branch: "feature", BranchDisplay: "feature", Tree: "clean", PR: &StatusPRFacts{Number: "7", URL: url, Head: head, Checks: "SUCCESS", Merge: "CLEAN", Review: "approved", ObservedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}}
	oldRepo := repo
	oldFacts := *repo.PR
	oldFacts.Checks = "FAILURE"
	oldFacts.Merge = "CONFLICTING"
	oldFacts.Review = "changes-requested"
	oldFacts.Draft = true
	oldFacts.Freshness = "live"
	oldFacts.ObservedAt = now.Format(time.RFC3339Nano)
	oldRepo.PR = &oldFacts
	current := StatusSnapshot{Collection: "fixture", Repos: []StatusRepo{oldRepo}, PRs: []StatusPRRow{{Repo: "widget", Slug: "example/widget", Number: "7", URL: &url, State: "DRAFT", Draft: true, HeadCommit: head, Title: "Retained title", Checks: statusString("FAILURE"), Merge: statusString("CONFLICTING"), Review: statusString("changes-requested"), Freshness: "live", ObservedAt: oldFacts.ObservedAt, worktreeDir: "widget"}}}
	repo.Next = &next.Step{}
	repo.Choices = &next.Choices{}
	incoming := StatusSnapshot{Collection: "fixture", Repos: []StatusRepo{repo}, PRs: []StatusPRRow{{Repo: "widget", Slug: "example/widget", Number: "7", URL: &url, State: "OPEN", HeadCommit: head, Checks: statusString("SUCCESS"), Merge: statusString("CLEAN"), Review: statusString("approved"), Freshness: "live", ObservedAt: repo.PR.ObservedAt, worktreeDir: "widget"}}}
	return incoming, current
}

func TestStatusHosted435RetainedRepoFactsMatchSameAssociation(t *testing.T) {
	incoming, current := hosted435Snapshots()
	got := StatusPreferNewerObservations(incoming, current)
	pr := got.Repos[0].PR
	if pr == nil || pr.Checks != "FAILURE" || pr.Merge != "CONFLICTING" || pr.Review != "changes-requested" || !pr.Draft || pr.Head != got.PRs[0].HeadCommit || pr.ObservedAt != got.PRs[0].ObservedAt {
		t.Fatalf("retained PR and repo disagree: row=%+v repo=%+v", got.PRs[0], pr)
	}
	if !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
		t.Fatal("retained display granted actions")
	}
}

func TestStatusHosted435RetainedMergeKeepsAuthorityAndRecheckDetail(t *testing.T) {
	incoming, current := hosted435Snapshots()
	current.PRs[0].State = "MERGED"
	current.PRs[0].Draft = false
	current.PRs[0].Merge = statusString("MERGED")
	current.PRs[0].Freshness = "recorded"
	current.Repos[0].PR = nil
	incoming.PRs[0].Freshness = "deferred"
	incoming.PRs[0].ObservationReason = "shared-budget"
	incoming.PRs[0].RetryAt = "2026-11-17T14:00:00Z"
	got := StatusPreferNewerObservations(incoming, current)
	if got.PRs[0].State != "MERGED" || got.PRs[0].Freshness != "recorded" || got.PRs[0].ObservedAt != current.PRs[0].ObservedAt || got.PRs[0].ObservationReason != "shared-budget" || got.PRs[0].RetryAt == "" {
		t.Fatalf("merge authority replaced by recheck outcome: %+v", got.PRs[0])
	}
	if got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil {
		t.Fatalf("older active repo facts survived terminal retention: %+v", got.Repos[0])
	}
}

func TestStatusHosted435AssociationMismatchCannotTransferFacts(t *testing.T) {
	for _, field := range []string{"collection", "directory", "worktree", "branch", "branch-kind", "head", "missing-head", "old-head", "slug", "forge", "row-worktree"} {
		t.Run(field, func(t *testing.T) {
			incoming, current := hosted435Snapshots()
			switch field {
			case "collection":
				incoming.Repos[0].Collection = "another"
			case "directory":
				incoming.Repos[0].Dir = "alias"
			case "worktree":
				incoming.Repos[0].Worktree = "checkout/another"
			case "branch":
				incoming.Repos[0].Branch = "new-feature"
			case "branch-kind":
				incoming.Repos[0].BranchKind = "detached"
			case "head":
				incoming.Repos[0].PR.Head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "missing-head":
				incoming.Repos[0].PR.Head = ""
			case "old-head":
				current.Repos[0].PR.Head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "slug":
				incoming.Repos[0].Slug = "example/another"
			case "forge":
				incoming.Repos[0].Forge = "bitbucket.org"
			case "row-worktree":
				current.PRs[0].worktreeDir = "another"
			}
			got := StatusPreferNewerObservations(incoming, current)
			if got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil || got.PRs[0].Title != "Retained title" {
				t.Fatalf("unproved %s association kept repo facts/actions: %+v", field, got.Repos[0])
			}
		})
	}
	for _, field := range []string{"number", "URL", "repository"} {
		t.Run("different-"+field, func(t *testing.T) {
			incoming, current := hosted435Snapshots()
			switch field {
			case "number":
				incoming.Repos[0].PR.Number = "8"
			case "URL":
				incoming.Repos[0].PR.URL = "https://github.com/example/another/pull/7"
			case "repository":
				incoming.Repos[0].Repo = "another"
			}
			want := incoming.Repos[0].PR
			got := StatusPreferNewerObservations(incoming, current)
			if got.Repos[0].PR != want || got.Repos[0].PR.Checks != "SUCCESS" {
				t.Fatal("unrelated repo/PR facts overwritten")
			}
		})
	}
}

func TestStatusHosted435RetainedUncertaintyKeepsAgeAndNoActions(t *testing.T) {
	for _, freshness := range []string{"deferred", "stale", "unavailable"} {
		t.Run(freshness, func(t *testing.T) {
			incoming, current := hosted435Snapshots()
			incoming.PRs[0].Freshness = freshness
			incoming.PRs[0].ObservationReason = "shared-budget"
			got := StatusPreferNewerObservations(incoming, current)
			if got.PRs[0].Freshness != freshness || got.Repos[0].PR == nil || got.Repos[0].PR.Freshness != freshness || got.Repos[0].PR.Checks != "FAILURE" || got.Repos[0].PR.ObservedAt != current.PRs[0].ObservedAt || got.Repos[0].Next != nil {
				t.Fatalf("row/repo uncertainty diverged: %+v %+v", got.PRs[0], got.Repos[0])
			}
		})
	}
}

func TestStatusHosted435RetainedMergeDoesNotCarryBranchAssociation(t *testing.T) {
	incoming, current := hosted435Snapshots()
	current.PRs[0].State = "MERGED"
	current.PRs[0].Merge = statusString("MERGED")
	current.PRs[0].Freshness = "stale"
	current.PRs[0].OnBranch = true
	current.PRs[0].Landing = &StatusLanding{State: "landed"}
	current.PRs[0].DisplayTitle = "MERGED still on old branch"
	current.Repos[0].PR = nil
	incoming.Repos[0].Branch = "different"
	incoming.PRs[0].Freshness = "unavailable"
	incoming.PRs[0].ObservationReason = "refresh-failed"
	got := StatusPreferNewerObservations(incoming, current)
	if got.PRs[0].State != "MERGED" || got.PRs[0].Freshness != "stale" || got.PRs[0].ObservedAt != current.PRs[0].ObservedAt || got.PRs[0].OnBranch || got.PRs[0].Landing != nil || got.PRs[0].DisplayTitle != got.PRs[0].Title || got.Repos[0].PR != nil {
		t.Fatalf("unproved merge association transferred: %+v", got)
	}
}

func TestStatusHosted435HistoricalMergeAssociationKeepsWarningButNoActions(t *testing.T) {
	incoming, current := hosted435Snapshots()
	current.PRs[0].State = "MERGED"
	current.PRs[0].Merge = statusString("MERGED")
	current.PRs[0].Freshness = "recorded"
	current.PRs[0].OnBranch = true
	current.PRs[0].Landing = &StatusLanding{State: "unknown", Reason: "inspect retained branch"}
	current.PRs[0].DisplayTitle = "MERGED — inspect retained branch"
	current.Repos[0].PR = nil
	current.Repos[0].Landing = current.PRs[0].Landing
	got := StatusPreferNewerObservations(incoming, current)
	if !got.PRs[0].OnBranch || (got.PRs[0].Landing == nil || got.PRs[0].Landing.State != "unknown") || got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
		t.Fatalf("proved association or action boundary lost: %+v", got)
	}
}

func TestStatusHosted435CanonicalMergedRepoKeepsHistoricalBranchWarning(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			incoming, current := hosted435Snapshots()
			current.PRs[0].State = "MERGED"
			current.PRs[0].Draft = false
			current.PRs[0].Checks = statusString("SUCCESS")
			current.PRs[0].Review = statusString("merged")
			current.PRs[0].StateSource = "registry"
			current.PRs[0].Merge = statusString("MERGED")
			current.PRs[0].Freshness = "recorded"
			current.PRs[0].OnBranch = true
			current.PRs[0].Landing = &StatusLanding{State: "landed", Reason: "prior landing"}
			current.Repos[0].PR = nil
			current.Repos[0].Landing = current.PRs[0].Landing
			if failed {
				incoming.PRs[0].State = "UNKNOWN"
				incoming.PRs[0].Freshness = "deferred"
				incoming.PRs[0].ObservationReason = "shared-budget"
				incoming.Repos[0].PR = nil
			}
			got := StatusPreferNewerObservations(incoming, current)
			if !got.PRs[0].OnBranch || got.PRs[0].Landing == nil || got.PRs[0].Landing.State != "unknown" || got.PRs[0].State != "MERGED" || got.PRs[0].ObservedAt != current.PRs[0].ObservedAt {
				t.Fatalf("canonical merged branch warning erased or falsely landed: %+v", got.PRs[0])
			}
			if got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
				t.Fatalf("historical warning supplied active facts/actions: %+v", got.Repos[0])
			}
		})
	}
}

func TestStatusHosted435RealFollowUpCommitKeepsHistoricalWarning(t *testing.T) {
	_, target, merge := squashFixture(t, true)
	recordedHead := fixtureGit(t, "-C", target.path, "rev-parse", "HEAD")
	fixtureFile(t, filepath.Join(target.path, "followup.txt"), "follow-up after recorded merge\n", 0600)
	fixtureGit(t, "-C", target.path, "add", "followup.txt")
	fixtureGit(t, "-C", target.path, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "follow-up after recorded merge")
	checkoutHead := fixtureGit(t, "-C", target.path, "rev-parse", "HEAD")
	if checkoutHead == recordedHead {
		t.Fatal("fixture did not create a real follow-up commit")
	}
	repo, err := statusLocalRepo(target)
	if err != nil {
		t.Fatal(err)
	}
	repo.Slug, repo.Forge = "example/widget", "github.com"
	landing := statusMergedLanding(repo, statusPRDetail{State: "MERGED", MergeCommit: merge, HeadCommit: recordedHead})
	if repo.PR != nil || repo.Branch != "topic" || landing.State != "follow-up" {
		t.Fatalf("not a canonical merged follow-up checkout: %+v %+v", repo, landing)
	}
	repo.Landing = landing
	url := "https://github.com/example/widget/pull/7"
	row := StatusPRRow{Repo: "widget", Slug: "example/widget", Number: "7", URL: &url, State: "MERGED", Merge: statusString("MERGED"), Freshness: "recorded", StateSource: "registry", ObservedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano), HeadCommit: recordedHead, MergeCommit: merge, OnBranch: true, Landing: landing, Title: "Recorded merged change", worktreeDir: repo.Dir}
	current := StatusSnapshot{Collection: repo.Collection, Repos: []StatusRepo{repo}, PRs: []StatusPRRow{row}}
	incomingRepo := repo
	incomingRepo.Landing = nil
	incomingRepo.Next, incomingRepo.Choices = &next.Step{}, &next.Choices{}
	incomingRow := row
	incomingRow.State, incomingRow.Freshness, incomingRow.Merge = "UNKNOWN", "deferred", nil
	incomingRow.ObservedAt = time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	incomingRow.OnBranch, incomingRow.Landing = false, nil
	got := StatusPreferNewerObservations(StatusSnapshot{Collection: repo.Collection, Repos: []StatusRepo{incomingRepo}, PRs: []StatusPRRow{incomingRow}}, current)
	if !got.PRs[0].OnBranch || got.PRs[0].Landing == nil || got.PRs[0].Landing.State != "unknown" || got.PRs[0].HeadCommit != recordedHead || got.PRs[0].ObservedAt != row.ObservedAt {
		t.Fatalf("real follow-up warning or recorded facts lost: %+v", got.PRs[0])
	}
	if got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
		t.Fatalf("historical warning granted active authority: %+v", got.Repos[0])
	}
	if fixtureGit(t, "-C", target.path, "rev-parse", "HEAD") != checkoutHead || fixtureGit(t, "-C", target.path, "branch", "--show-current") != "topic" {
		t.Fatal("presentation mutated the real checkout")
	}
}
