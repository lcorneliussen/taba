package next

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Choice keeps unavailable alternatives visible without making them executable.
// The key binds every action to the same complete input, including unpublished
// work and remote evidence; next projects the recommendation and shares its key.
type Choice struct {
	Step
	Available bool   `json:"available"`
	Blocked   string `json:"blocked,omitempty"`
}

type Choices struct {
	Recommended string   `json:"recommended,omitempty"`
	Actions     []Choice `json:"actions"`
}

func (c Choices) Primary() *Step {
	for _, a := range c.Actions {
		if a.Available && a.Key == c.Recommended {
			s := a.Step
			return &s
		}
	}
	return nil
}

// Select accepts the exact displayed alternative, never a replacement selected
// by its row position. Callers must collect fresh facts before calling it.
func (c Choices) Select(key string) *Step {
	for _, a := range c.Actions {
		if a.Available && a.Key == key {
			s := a.Step
			return &s
		}
	}
	return nil
}

// ChoicesFor adds explicit choices to the single-step compatibility planner.
// This slice uses existing commands and skill handoffs. Review jobs, claims,
// merge policy discovery and automatic continuation belong to their adapters.
func ChoicesFor(f Facts) Choices {
	raw, _ := json.Marshal(f)
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	c := Choices{Actions: []Choice{}}
	add := func(s Step, blocked string, primary bool) {
		for _, a := range c.Actions {
			if a.Step.Step == s.Step {
				return
			}
		}
		s.Repo = f.Repo
		s.Key = f.Collection + ":" + f.Repo + ":" + s.Step + ":" + digest
		c.Actions = append(c.Actions, Choice{Step: s, Available: blocked == "", Blocked: blocked})
		if primary && blocked == "" {
			c.Recommended = s.Key
		}
	}
	// Local unpublished fixes supersede failures observed on the remote PR.
	// Conflicts and unfinished operations still have to be settled first.
	primaryFacts := f
	if f.ToPush > 0 && f.PR != nil && f.PR.State == "open" && f.PR.Mergeable != "conflicting" && f.Behind == 0 {
		pr := *f.PR
		pr.Checks = ""
		pr.Review = ""
		primaryFacts.PR = &pr
	}
	if s := For(primaryFacts); s != nil {
		blocked := ""
		if s.Step == "merge-pr" && (f.PR.Mergeable != "mergeable" || (f.PR.Head != "" && f.PR.Head != f.Head) || f.ToPush > 0 || f.Behind > 0) {
			blocked = "Mergeability is unknown; inspect the pull request's current requirements."
		}
		add(*s, blocked, true)
	}
	if f.InProgress != "" || f.Conflicted > 0 {
		return c
	}
	if f.Dirty > 0 && f.PR != nil && f.PR.State == "merged" {
		add(Step{Step: "new-branch", By: ByAgent, Skill: "taba-catch-up", Reason: "Preserve intended uncommitted work on a new branch; the merged branch is finished."}, "", true)
		return c
	}
	if f.Dirty > 0 {
		add(Step{Step: "commit", By: ByAgent, Skill: "wtc", Reason: "Review intended changes and save local commits. Pushing and opening a PR are separate."}, "", true)
		if f.Host == HostOpenClaw && f.Behind > 0 && !(f.Branch == "" && f.Ahead > 0) {
			conflicts := len(f.MergeConflicts) > 0 || f.PR != nil && f.PR.Mergeable == "conflicting"
			add(Step{Step: "catch-up", By: ByWTC, Approve: ApproveAgent, Command: []string{"wtc", "catch-up"}, Reason: "Catch up with the base, preserving local changes and their staging in a recovery copy. Restore them after the merge; conflicts need the owning conversation."}, "", !conflicts)
			if conflicts {
				add(Step{Step: "merge", By: ByAgent, Skill: "wtc-catch-up", Reason: "The base merge conflicts. Before git merge, preserve restored local edits and their staging in a separately pinned Git stash, verify the checkout is clean, then merge and resolve the base. Reconcile the saved staged, unstaged and untracked edits afterwards; do not publish or discard the backup. Follow the bundled recovery procedure in the owning environment."}, "", true)
			}
		}
		return c
	}
	pr := f.PR
	if pr == nil || pr.State != "open" {
		return c
	}
	// A forge approval is not evidence of a current independent review. The
	// follow skill rechecks reviews, requirements and ownership before acting.
	follow := Step{Step: "follow-pr", By: ByAgent, Skill: "taba-follow", Reason: fmt.Sprintf("Check #%d's current reviews, requirements and next blocker. Independent review execution is a separate action.", pr.Number)}
	add(follow, "", c.Recommended == "")
	reviewBlocked := ""
	if f.ToPush > 0 {
		reviewBlocked = "Publish intended commits before reviewing the remote PR head."
	}
	if f.Behind > 0 || pr.Mergeable == "conflicting" {
		reviewBlocked = "Settle the base merge before reviewing this checkout."
	}
	if f.Host == HostOpenClaw {
		reviewBlocked = "Independent review jobs on this host require the shared review adapter. Use Check PR progress for a handoff."
	}
	add(Step{Step: "review", By: ByAgent, Skill: "taba-local-review", Reason: fmt.Sprintf("Review #%d independently; check existing review activity and ownership before starting.", pr.Number)}, reviewBlocked, false)
	mergeBlocked := ""
	switch {
	case pr.Head != "" && pr.Head != f.Head:
		mergeBlocked = "The checkout and remote PR head differ; refresh and inspect the current head."
	case pr.Draft:
		mergeBlocked = "The PR is a draft; readiness requires a current independent review."
	case f.ToPush > 0:
		mergeBlocked = "Local commits have not been published."
	case f.Behind > 0:
		mergeBlocked = "Bring the branch up to date first."
	case pr.Checks != "success":
		mergeBlocked = "Checks are pending, failing, absent or unavailable."
	case pr.Review != "approved":
		mergeBlocked = "Approval and merge requirements have not been established."
	case pr.Mergeable != "mergeable":
		mergeBlocked = "Mergeability is conflicting or unknown."
	}
	add(Step{Step: "merge-pr", By: ByPerson, Reason: "Open the pull request and inspect all current merge requirements before merging."}, mergeBlocked, false)
	return c
}
