package openclaw

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Completed rounds belong to the worktree, outside the checkout. Their PRs
// remain on the forge; this record keeps them in the collection's PR list
// after HEAD leaves their branches.
type completedRounds struct {
	Origin Origin        `json:"origin"`
	Base   string        `json:"base"`
	PRs    []PullRequest `json:"pullRequests"`
}

const roundsFile = "wtc-completed-rounds.json"

func readCompletedRounds(dir string) (completedRounds, error) {
	var rounds completedRounds
	p, err := git(dir, "rev-parse", "--path-format=absolute", "--git-path", roundsFile)
	if err != nil {
		return rounds, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return rounds, nil
	}
	if err != nil {
		return rounds, err
	}
	if err := json.Unmarshal(data, &rounds); err != nil {
		return completedRounds{}, fmt.Errorf("cannot read completed pull requests: %w", err)
	}
	url, err := git(dir, "remote", "get-url", "origin")
	if err != nil || ParseOrigin(url) != rounds.Origin {
		return completedRounds{}, fmt.Errorf("completed pull requests belong to another origin")
	}
	return rounds, nil
}

func (c *Checkout) rememberCompleted(pr PullRequest) error {
	rounds, err := readCompletedRounds(c.Dir)
	if err != nil {
		return err
	}
	url, err := git(c.Dir, "remote", "get-url", "origin")
	if err != nil {
		return err
	}
	rounds.Origin, rounds.Base = ParseOrigin(url), c.Dest
	found := false
	for i, old := range rounds.PRs {
		if old.Number == pr.Number {
			rounds.PRs[i], found = pr, true
			break
		}
	}
	if !found {
		rounds.PRs = append(rounds.PRs, pr)
	}
	data, err := json.MarshalIndent(rounds, "", "  ")
	if err != nil {
		return err
	}
	p, err := git(c.Dir, "rev-parse", "--path-format=absolute", "--git-path", roundsFile)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".wtc-rounds-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// returnToTip never merges into or rewrites a completed branch. The branch
// itself is retained, including after a squash merge. Extra commits must be
// moved to a new branch by the agent before anything leaves this branch.
func (c *Checkout) returnToTip(result CatchUp, pr *PullRequest) (CatchUp, error) {
	ref := "refs/remotes/origin/" + c.Dest
	if !gitOK(c.Dir, "merge-base", "--is-ancestor", "HEAD", ref) {
		if pr.MergeCommit == "" || !gitOK(c.Dir, "merge-base", "--is-ancestor", pr.MergeCommit, ref) ||
			pr.Head == "" || !gitOK(c.Dir, "merge-base", "--is-ancestor", pr.Head, "HEAD") {
			return result, fmt.Errorf("#%d was merged, but its work is not proven on origin/%s: keep this branch and put any follow-up work on a new branch first", pr.Number, c.Dest)
		}
		work, err := c.hasFollowUpWork(pr, ref)
		if err != nil {
			return result, err
		}
		if work {
			// Older catch-up merged the base into the completed branch. It
			// is safe to leave those merges only when they contain no new
			// work or resolution edits. Compare with each merge's own base,
			// since origin may have advanced again after the old catch-up.
			return result, fmt.Errorf("#%d was merged, but this branch has follow-up work: put it on a new branch before returning to the tip", pr.Number)
		}
	}
	// Keep the PR before moving HEAD, including when checkout later refuses
	// an untracked path. A retry remains safe and the list deduplicates it.
	if err := c.rememberCompleted(*pr); err != nil {
		return result, err
	}
	if err := c.protectIgnored(ref); err != nil {
		return result, err
	}
	if _, err := git(c.Dir, "checkout", "--quiet", "--no-overwrite-ignore", "--detach", ref); err != nil {
		return result, fmt.Errorf("returning to origin/%s's tip: %w", c.Dest, err)
	}
	result.Branch, result.ReturnedToTip = "", true
	result.Head, _ = git(c.Dir, "rev-parse", "HEAD")
	result.Note = fmt.Sprintf("#%d is in the pull request list; the next change starts a new branch", pr.Number)
	return result, nil
}

// hasFollowUpWork is shared by status and execution: resolution edits in a
// merge are work too, even when there are no new one-parent commits.
func (c *Checkout) hasFollowUpWork(pr *PullRequest, base string) (bool, error) {
	commits, err := git(c.Dir, "rev-list", "--parents", "HEAD", "^"+pr.Head, "^"+base)
	if err != nil {
		return false, err
	}
	return commits != "" && !c.onlyBaseMerges(commits, base), nil
}

func (c *Checkout) onlyBaseMerges(commits, base string) bool {
	for _, line := range strings.Split(commits, "\n") {
		p := strings.Fields(line)
		if len(p) != 3 || !gitOK(c.Dir, "merge-base", "--is-ancestor", p[2], base) {
			return false
		}
		tree, err := git(c.Dir, "rev-parse", p[0]+"^{tree}")
		if err != nil {
			return false
		}
		if theirs, err := git(c.Dir, "rev-parse", p[2]+"^{tree}"); err == nil && theirs == tree {
			continue
		}
		predicted, err := git(c.Dir, "merge-tree", "--write-tree", "--no-messages", p[1], p[2])
		if err != nil || predicted != tree {
			return false
		}
	}
	return true
}
