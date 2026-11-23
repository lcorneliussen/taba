package main

import (
	"errors"
	"testing"
	"time"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// The card's pull request and its refusal both carry the read identity and
// when the read completed, which news uses to key and fence (#471).
func TestBitbucketCardCarriesReadIdentityAndTime(t *testing.T) {
	at := time.Date(2026, 11, 23, 10, 0, 0, 500_000_000, time.UTC)
	pr, refusal, _ := bitbucketCardPullRequest(wtc.BitbucketBranchReadResult{
		PR: &wtc.BitbucketCheckoutPR{Number: 7, State: "open"}, Identity: "k:0123456789abcdef", ObservedAt: at}, t.TempDir(), "feature")
	if refusal != nil || pr == nil || pr.ReadIdentity != "k:0123456789abcdef" || pr.ObservedAt != "2026-11-23T10:00:00.5Z" || pr.Branch != "feature" {
		t.Fatalf("pull request = %+v, refusal = %+v", pr, refusal)
	}
	var fresh *openclaw.ReadFreshness
	pr, refusal, fresh = bitbucketCardPullRequest(wtc.BitbucketBranchReadResult{
		Err: errors.New("twg bitbucket pull-requests query: twg is not installed"), Identity: "u:fedcba9876543210", ObservedAt: at}, t.TempDir(), "feature")
	if pr != nil || refusal == nil || refusal.Cause != wtc.RefusalClient || !refusal.Access || refusal.ReadIdentity != "u:fedcba9876543210" || refusal.ObservedAt != "2026-11-23T10:00:00.5Z" || fresh == nil || fresh.ReadIdentity != "u:fedcba9876543210" {
		t.Fatalf("refusal = %+v", refusal)
	}
	if pr, refusal, fresh := bitbucketCardPullRequest(wtc.BitbucketBranchReadResult{Identity: "k:x", ObservedAt: at}, t.TempDir(), "feature"); pr != nil || refusal != nil || fresh == nil || fresh.ObservedAt != "2026-11-23T10:00:00.5Z" || fresh.ReadIdentity != "k:x" {
		t.Fatalf("no pull request is neither a pull request nor a refusal, but says who read it and when: %v %v %+v", pr, refusal, fresh)
	}
}
