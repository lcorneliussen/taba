// Package next reads a repository's next step from its state: what should
// happen to it now, who does it and who must agree first. Nothing remembers a
// flow: once a step is done, the state says what follows, so the next status
// shows the next step.
package next

import (
	"fmt"
	"os/exec"
	"strings"
)

// Who does a step, and who must agree to it first.
const (
	ByWTC    = "wtc"    // deterministic: a wtc command, or the gateway's own action
	ByAgent  = "agent"  // needs judgment: a skill the agent follows
	ByPerson = "person" // only people do it: merging a pull request

	ApprovePerson = "person" // needs the person's authority: the gateway's push
	ApproveAgent  = "agent"  // wtc could do it, but the agent decides when
)

// Hosts a step is read for: the same push is the gateway's on OpenClaw and
// the agent's locally.
const (
	HostOpenClaw = "openclaw"
	HostLocal    = "local"
)

// Step is a repository's next step.
type Step struct {
	Repo string `json:"repo"`
	// Step names it: resolve, return-to-tip, new-branch, merge, catch-up,
	// fix-checks, address-review, push, merge-pr.
	Step    string `json:"step"`
	By      string `json:"by"`
	Approve string `json:"approve,omitempty"`
	Reason  string `json:"reason"`
	// What carries it out: the skill an agent follows, the wtc command, or
	// the gateway's own action.
	Skill   string   `json:"skill,omitempty"`
	Command []string `json:"command,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	// Then names what usually follows, as a hint.
	Then []string `json:"then,omitempty"`
	// Key identifies the fact, so that a reactor acts on it once, not on
	// every refresh: the step and the commits it is about.
	Key string `json:"key"`
}

// PR is what a step needs of a repository's pull request.
type PR struct {
	Number    int
	Head      string // exact remote PR head, when observed
	State     string // open, merged, closed
	Draft     bool
	Checks    string // success, failure, pending, none
	Mergeable string // mergeable, conflicting, unknown
	Review    string // approved, changes_requested, review_required
	// MergedIn says whether the checkout has the merge commit of a merged
	// pull request; nil when unknown.
	MergedIn *bool
}

// Facts is a repository's state, as a status reads it.
type Facts struct {
	Collection string // local collection targeted by executable commands
	Repo       string
	Host       string
	Branch     string // empty when detached
	Base       string // the ref Behind counts against, e.g. origin/main
	Head       string // the commit checked out
	// BaseHead is the commit Base is at.
	BaseHead   string
	Ahead      int
	Behind     int
	Dirty      int
	Conflicted int
	// InProgress is a merge, rebase or the like that is not finished.
	InProgress string
	// MergeConflicts are the files merging Base would conflict in, as
	// PredictMerge says; MergeUnknown when it could not tell.
	MergeConflicts []string
	MergeUnknown   bool
	// ToPush counts the commits a push would send.
	ToPush int
	// AfterMerge counts the commits made after the pull request merged:
	// neither in it nor in the base, and no merges.
	AfterMerge int
	// NeedsBranch preserves work on a completed branch when the local
	// catch-up cannot prove that the base carries it.
	NeedsBranch bool
	PR          *PR
}

// For is the repository's next step, or nil when nothing waits on anyone.
// Uncommitted files are work in progress: then only an unfinished merge is
// a step, and everything else waits for the work. Checks still running
// hold back only merging the pull request.
func For(f Facts) *Step {
	s := &Step{Repo: f.Repo}
	key := func(parts ...string) string {
		prefix := []string{f.Repo, s.Step}
		if f.Collection != "" {
			prefix = append([]string{f.Collection}, prefix...)
		}
		return strings.Join(append(prefix, parts...), ":")
	}
	pr := f.PR
	open := pr != nil && pr.State == "open"
	switch {
	case f.InProgress != "" || f.Conflicted > 0:
		what := f.InProgress
		if what == "" {
			what = "a merge"
		}
		s.Step, s.By, s.Skill = "resolve", ByAgent, "taba-catch-up"
		s.Reason = fmt.Sprintf("%s is not finished in %s", what, f.Repo)
		if f.Conflicted > 0 {
			s.Reason += fmt.Sprintf(": %d conflicted files", f.Conflicted)
		}
		s.Then = []string{"push"}
		s.Key = key(f.Head)
	case f.Dirty > 0:
		return nil
	// A merged branch is finished: nothing is merged into it any more. It
	// goes back to the tip, and the next change branches anew; commits made
	// after the merge are such a change, on the wrong branch.
	case pr != nil && pr.State == "merged" && (f.AfterMerge > 0 || f.NeedsBranch):
		s.Step, s.By, s.Skill = "new-branch", ByAgent, "taba-catch-up"
		s.Reason = fmt.Sprintf("#%d was merged, but %d commits came after it: put them on a new branch", pr.Number, f.AfterMerge)
		if f.NeedsBranch {
			s.Reason = fmt.Sprintf("#%d was merged, but the base does not carry this branch's work: put it on a new branch", pr.Number)
		}
		s.Then = []string{"push"}
		s.Key = key(f.Head)
	case pr != nil && pr.State == "merged" && (f.Branch != "" || f.Behind > 0 || pr.MergedIn == nil || !*pr.MergedIn):
		s.Step, s.By = "return-to-tip", ByWTC
		s.Reason = fmt.Sprintf("#%d was merged: back to %s's tip; the next change branches anew", pr.Number, baseName(f.Base))
		s.Command = []string{"wtc", "catch-up"}
		s.Key = key(fmt.Sprint(pr.Number), f.BaseHead)
	case pr != nil && pr.State == "merged":
		// Completed, with no further work to publish: never push to a
		// merged branch.
		return nil
	case f.Branch == "" && f.Ahead > 0:
		s.Step, s.By, s.Skill = "new-branch", ByAgent, "taba-catch-up"
		s.Reason = fmt.Sprintf("%d commits on no branch: put them on a branch before anything moves", f.Ahead)
		s.Key = key(f.Head)
	case open && pr.Mergeable == "conflicting":
		s.Step, s.By, s.Skill = "merge", ByAgent, "taba-catch-up"
		s.Reason = fmt.Sprintf("#%d conflicts with %s", pr.Number, baseName(f.Base))
		s.Then = []string{"push"}
		s.Key = key(f.Head, f.BaseHead)
	case open && pr.Checks == "failure":
		s.Step, s.By, s.Skill = "fix-checks", ByAgent, "taba-follow"
		s.Reason = fmt.Sprintf("checks failed on #%d", pr.Number)
		s.Then = []string{"push"}
		s.Key = key(f.Head)
	case open && pr.Review == "changes_requested":
		s.Step, s.By, s.Skill = "address-review", ByAgent, "taba-follow"
		s.Reason = fmt.Sprintf("changes were requested on #%d", pr.Number)
		s.Then = []string{"push"}
		s.Key = key(f.Head)
	case f.Behind > 0 && len(f.MergeConflicts) > 0:
		s.Step, s.By, s.Skill = "merge", ByAgent, "taba-catch-up"
		s.Reason = fmt.Sprintf("%d new on %s; merging conflicts in %s", f.Behind, baseName(f.Base), strings.Join(f.MergeConflicts, ", "))
		s.Then = []string{"push"}
		s.Key = key(f.Head, f.BaseHead)
	case f.Behind > 0:
		s.Step, s.By = "catch-up", ByWTC
		s.Command = []string{"wtc", "catch-up"}
		s.Reason = fmt.Sprintf("%d new on %s", f.Behind, baseName(f.Base))
		switch {
		case f.MergeUnknown:
			s.Reason += "; whether merging conflicts is not known"
			s.Approve = ApproveAgent
		case f.Branch != "":
			// A clean merge into a branch is still the agent's call: it
			// decides when its work takes the base in (and locally a catch-up
			// pushes the merge to an open pull request). Only a detached
			// checkout just moves: one with commits of its own was sent to a
			// new branch above.
			s.Reason += "; merges cleanly"
			s.Approve = ApproveAgent
		}
		s.Key = key(f.Head, f.BaseHead)
	case f.ToPush > 0:
		s.Step = "push"
		s.Reason = fmt.Sprintf("%d commits to push", f.ToPush)
		if pr == nil || pr.State != "open" {
			s.Reason += ", opening a draft pull request"
		}
		if f.Host == HostOpenClaw {
			// The gateway pushes with the person's account, after they confirm.
			s.By, s.Approve, s.Gateway = ByWTC, ApprovePerson, "github_publish"
		} else {
			s.By, s.Skill = ByAgent, "taba-draft-pr"
		}
		s.Then = []string{"follow"}
		s.Key = key(f.Head)
	case open && !pr.Draft && pr.Checks == "success" && pr.Review == "approved" && pr.Mergeable != "conflicting":
		s.Step, s.By = "merge-pr", ByPerson
		s.Reason = fmt.Sprintf("#%d is approved and green: people merge it", pr.Number)
		s.Then = []string{"catch-up"}
		s.Key = key(f.Head)
	default:
		return nil
	}
	if f.Host != HostOpenClaw && len(s.Command) > 0 {
		if f.Collection != "" {
			s.Command = append(s.Command, f.Collection)
		}
		s.Command = append(s.Command, "--repos", f.Repo)
	}
	return s
}

func baseName(base string) string {
	if base == "" {
		return "the base"
	}
	return strings.TrimPrefix(base, "origin/")
}

// PredictMerge says which files merging ref into dir's HEAD would conflict
// in, without touching the worktree or the index (git merge-tree). ok is
// false when git cannot tell (older than 2.38, or an unrelated history).
func PredictMerge(dir, ref string) (conflicts []string, ok bool) {
	return PredictMergeAt(dir, "HEAD", ref)
}

// PredictMergeAt predicts a merge of two commits independently of the checked
// out branch. Both refs are resolved before prediction; unavailable objects or
// unsupported Git versions leave the result unknown.
func PredictMergeAt(dir, head, base string) (conflicts []string, ok bool) {
	commits := make([]string, 0, 2)
	for _, ref := range []string{head, base} {
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}").Output()
		if err != nil {
			return nil, false
		}
		commits = append(commits, strings.TrimSpace(string(out)))
	}
	out, err := exec.Command("git", "-C", dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", commits[0], commits[1]).Output()
	if err == nil {
		return nil, true
	}
	exit, isExit := err.(*exec.ExitError)
	if !isExit || exit.ExitCode() != 1 {
		return nil, false
	}
	// Exit 1: the first NUL-delimited field is the tree, then conflicted paths.
	fields := strings.Split(string(out), "\x00")
	for _, path := range fields[1:] {
		if path != "" {
			conflicts = append(conflicts, path)
		}
	}
	// A conflict exit without paths supplies no usable observation. Normal Git
	// conflicts list paths; malformed or unsupported output stays unknown.
	return conflicts, len(conflicts) > 0
}
