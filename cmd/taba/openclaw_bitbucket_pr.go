package main

import (
	"os/exec"
	"time"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// The gateway's Collection card and pull request news read a Bitbucket
// checkout's pull request through taba's Bitbucket reader: TWG with the
// host's read credential where configured. The deprecated bb client cannot
// look a pull request up by branch here and reports that as a refusal. A read that
// fails is reported with its classified cause on the card and, once per
// cause, in the news; it is never shown as "no pull request".
func init() {
	openclaw.BitbucketPullRequest = func(dir, owner, repo, branch string) (*openclaw.PullRequest, *openclaw.ReadRefusal) {
		read := wtc.BitbucketBranchRead("", owner+"/"+repo, branch)
		observed := read.ObservedAt.Format(time.RFC3339Nano)
		found, err := read.PR, read.Err
		if err != nil {
			r := wtc.ClassifyBitbucketRefusal(err, wtc.BitbucketReadCredentialLabel(""))
			return nil, &openclaw.ReadRefusal{Cause: r.Cause, Status: r.Status, Message: r.Message, Access: r.Access(), ReadIdentity: read.Identity, ObservedAt: observed}
		}
		if found == nil {
			return nil, nil
		}
		pr := &openclaw.PullRequest{Number: found.Number, URL: found.URL, State: found.State, Draft: found.Draft, Title: found.Title,
			Checks: found.Checks, Review: found.Review, Head: found.Head, Branch: branch, MergeCommit: found.MergeCommit,
			ReadIdentity: read.Identity, ObservedAt: observed}
		if pr.State == "merged" && pr.MergeCommit != "" {
			in := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", pr.MergeCommit, "HEAD").Run() == nil
			pr.MergedIn = &in
		}
		return pr, nil
	}
}
