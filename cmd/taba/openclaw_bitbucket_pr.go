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
	openclaw.BitbucketPullRequest = func(dir, owner, repo, branch string) (*openclaw.PullRequest, *openclaw.ReadRefusal, *openclaw.ReadFreshness) {
		return bitbucketCardPullRequest(wtc.BitbucketBranchRead("", owner+"/"+repo, branch), dir, branch)
	}
}

// bitbucketCardPullRequest maps one branch read to the card's pull request
// or refusal, both with the read identity and when the read completed, and
// returns that freshness for every outcome, also when there is no pull
// request.
func bitbucketCardPullRequest(read wtc.BitbucketBranchReadResult, dir, branch string) (*openclaw.PullRequest, *openclaw.ReadRefusal, *openclaw.ReadFreshness) {
	observed := read.ObservedAt.Format(time.RFC3339Nano)
	fresh := &openclaw.ReadFreshness{ReadIdentity: read.Identity, ObservedAt: observed}
	if read.Err != nil {
		r := wtc.ClassifyBitbucketRefusal(read.Err, wtc.BitbucketReadCredentialLabel(""))
		return nil, &openclaw.ReadRefusal{Cause: r.Cause, Status: r.Status, Message: r.Message, Access: r.Access(), ReadIdentity: read.Identity, ObservedAt: observed}, fresh
	}
	found := read.PR
	if found == nil {
		return nil, nil, fresh
	}
	pr := &openclaw.PullRequest{Number: found.Number, URL: found.URL, State: found.State, Draft: found.Draft, Title: found.Title,
		Checks: found.Checks, Review: found.Review, Head: found.Head, Branch: branch, MergeCommit: found.MergeCommit,
		ReadIdentity: read.Identity, ObservedAt: observed}
	if pr.State == "merged" && pr.MergeCommit != "" {
		in := exec.Command("git", "-C", dir, "merge-base", "--is-ancestor", pr.MergeCommit, "HEAD").Run() == nil
		pr.MergedIn = &in
	}
	return pr, nil, fresh
}
