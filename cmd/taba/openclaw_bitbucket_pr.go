package main

import (
	"os/exec"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// The gateway's Collection card and pull request news read a Bitbucket
// checkout's pull request through taba's Bitbucket reader: TWG with the
// host's read credential where configured, never bb (#471). A read that
// fails leaves the checkout without a pull request, which the news watcher
// treats as "could not be asked", never as a change.
func init() {
	openclaw.BitbucketPullRequest = func(dir, owner, repo, branch string) *openclaw.PullRequest {
		found, err := wtc.BitbucketBranchPullRequest("", owner+"/"+repo, branch)
		if err != nil || found == nil {
			return nil
		}
		pr := &openclaw.PullRequest{Number: found.Number, URL: found.URL, State: found.State, Draft: found.Draft, Title: found.Title,
			Checks: found.Checks, Review: found.Review, Head: found.Head, Branch: branch, MergeCommit: found.MergeCommit}
		if pr.State == "merged" && pr.MergeCommit != "" {
			in := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", pr.MergeCommit, "HEAD").Run() == nil
			pr.MergedIn = &in
		}
		return pr
	}
}
