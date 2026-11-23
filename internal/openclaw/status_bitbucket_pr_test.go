package openclaw

import (
	"os/exec"
	"testing"
)

func TestCheckoutStatusReadsBitbucketPullRequestsThroughTheHook(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "remote", "add", "origin", "https://bitbucket.org/example-ws/widget.git"},
		{"-C", dir, "-c", "user.name=T", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "start"}, {"-C", dir, "switch", "-qc", "feature"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	old := BitbucketPullRequest
	t.Cleanup(func() { BitbucketPullRequest = old })
	var asked string
	BitbucketPullRequest = func(_, owner, repo, branch string) (*PullRequest, *ReadRefusal, *ReadFreshness) {
		asked = owner + "/" + repo + "@" + branch
		return &PullRequest{Number: 7, State: "open", Checks: "success"}, nil, &ReadFreshness{ReadIdentity: "k:a", ObservedAt: "2026-11-23T10:00:00Z"}
	}
	s := checkoutStatus(dir, true)
	if asked != "example-ws/widget@feature" || s.PullRequest == nil || s.PullRequest.Number != 7 || s.PullRequestRead == nil || s.PullRequestRead.ObservedAt != "2026-11-23T10:00:00Z" {
		t.Fatalf("asked %q, pull request %+v", asked, s.PullRequest)
	}
	asked = ""
	if s := checkoutStatus(dir, false); asked != "" || s.PullRequest != nil {
		t.Fatal("a status without pull requests must not read one")
	}
}
