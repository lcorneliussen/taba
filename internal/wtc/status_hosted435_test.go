package wtc

import (
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

func TestStatusHosted435ProvedMergeAssociationKeepsLandingButNoActions(t *testing.T) {
	incoming, current := hosted435Snapshots()
	current.PRs[0].State = "MERGED"
	current.PRs[0].Merge = statusString("MERGED")
	current.PRs[0].Freshness = "recorded"
	current.PRs[0].OnBranch = true
	current.PRs[0].Landing = &StatusLanding{State: "unknown", Reason: "inspect retained branch"}
	current.PRs[0].DisplayTitle = "MERGED — inspect retained branch"
	got := StatusPreferNewerObservations(incoming, current)
	if !got.PRs[0].OnBranch || got.PRs[0].Landing != current.PRs[0].Landing || got.Repos[0].PR != nil || !got.Repos[0].PRUnconfirmed || got.Repos[0].Next != nil || got.Repos[0].Choices != nil {
		t.Fatalf("proved association or action boundary lost: %+v", got)
	}
}
