package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayPublicationEnlistsCapturedBranchAndPreservesOtherRows(t *testing.T) {
	c, checkout := enlistedIdentityFixture(t, true)
	_, err := c.EnlistPR(PRRecord{Repo: "other", Number: "9", URL: "https://example.invalid/untouched", Branch: "old"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(c.PRFile())
	// An already returned-to-base checkout must still record the published branch.
	fixtureGit(t, "-C", checkout, "checkout", "--detach", "-q")
	input := PRRecord{URL: "https://github.com/example/widget/pull/42", Branch: "feature/published"}
	got, err := c.EnlistGatewayPublication(checkout, input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repo != "widget" || got.Number != "42" || got.Branch != input.Branch {
		t.Fatalf("wrong receipt: %+v", got)
	}
	after, _ := os.ReadFile(c.PRFile())
	if !strings.Contains(string(after), "other 9 old https://example.invalid/untouched") || !strings.Contains(string(before), "other") {
		t.Fatalf("other row changed: %s", after)
	}
	if _, err = c.EnlistGatewayPublication(checkout, input); err != nil {
		t.Fatal(err)
	}
	records, err := c.ListPRs()
	if err != nil || len(records) != 2 {
		t.Fatalf("retry not idempotent: %+v %v", records, err)
	}
}

func TestGatewayPublicationRejectsForeignOrChangedIdentity(t *testing.T) {
	c, checkout := enlistedIdentityFixture(t, true)
	for _, url := range []string{"https://github.com/other/widget/pull/42", "https://github.com/example/widget/pull/0", "https://github.com/example/widget/pull/42?x=1", "https://user@github.com/example/widget/pull/42", "https://github.com:443/example/widget/pull/42", "https://github.com/example/widget/pull/42/", "https://bitbucket.org/example/widget/pull-requests/42"} {
		if _, err := c.EnlistGatewayPublication(checkout, PRRecord{URL: url, Branch: "feature"}); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	if _, err := c.EnlistGatewayPublication(t.TempDir(), PRRecord{URL: "https://github.com/example/widget/pull/42", Branch: "feature"}); err == nil {
		t.Fatal("accepted foreign worktree")
	}
	fixtureGit(t, "-C", checkout, "remote", "set-url", "origin", "https://github.com/other/widget.git")
	if _, err := c.EnlistGatewayPublication(checkout, PRRecord{URL: "https://github.com/example/widget/pull/42", Branch: "feature"}); err == nil {
		t.Fatal("accepted changed origin")
	}
	if _, err := os.Stat(filepath.Join(c.Collection, ".wtc-prs")); !os.IsNotExist(err) {
		t.Fatal("rejection wrote registry")
	}
}

func TestGatewayPublicationBitbucketAndInvalidBranches(t *testing.T) {
	c, checkout := enlistedIdentityFixture(t, true)
	remote := "https://bitbucket.org/example/widget.git"
	fixtureGit(t, "-C", checkout, "remote", "set-url", "origin", remote)
	for i := range c.Registry.Repos {
		if c.Registry.Repos[i].Name == "widget" {
			c.Registry.Repos[i].Remote = remote
		}
	}
	input := PRRecord{URL: "https://bitbucket.org/example/widget/pull-requests/42", Branch: "feature/published"}
	if got, err := c.EnlistGatewayPublication(checkout, input); err != nil || got.Repo != "widget" || got.Number != "42" {
		t.Fatalf("Bitbucket enlist: %+v %v", got, err)
	}
	for _, url := range []string{"https://bitbucket.org/example/widget/pull/42", "https://bitbucket.org/Example/widget/pull-requests/42", "https://bitbucket.org/example/Widget/pull-requests/42"} {
		bad := input
		bad.URL = url
		if _, err := c.EnlistGatewayPublication(checkout, bad); err == nil {
			t.Fatalf("accepted changed Bitbucket identity %s", url)
		}
	}
	for _, branch := range []string{"", "-bad", "has space", "bad..ref", "bad\nref", "@{-1}"} {
		bad := input
		bad.Branch = branch
		if _, err := c.EnlistGatewayPublication(checkout, bad); err == nil {
			t.Fatalf("accepted invalid branch %q", branch)
		}
	}
}
