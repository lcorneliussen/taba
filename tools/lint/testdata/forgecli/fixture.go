// Package fixture holds forge CLI calls taken from the parents of past fixes.
// tools/lint/check-rules.sh asserts that the rules flag exactly the lines
// marked "want forgecli", so the rule keeps catching them.
package fixture

import (
	"context"
	"os/exec"
)

func ForgeCalls(ctx context.Context, pr, slug, number, owner, program string, args []string) []*exec.Cmd {
	return []*exec.Cmd{
		// f9bd5af^ review_bundle.go:117, which the #126 fix left on a pipe
		// and which was still on one at ca70d65.
		exec.Command("gh", "pr", "view", pr, "--repo", slug, "--json", "title,body"), // want forgecli
		// f9bd5af^ review_bundle.go:119, read at :121 by cmd.Output().
		exec.Command("bb", "pr", "view", pr, "--json"), // want forgecli
		// f9bd5af^ review_status.go:228, read at :230.
		exec.Command("bb", "pr", "comments", "list", number, "--all", "--json"), // want forgecli
		// 687e867 upgrade.go, added after #126 was fixed.
		exec.Command("gh", "api", "repos/"+owner+"/releases/latest", "--jq", ".tag_name"), // want forgecli
		exec.CommandContext(ctx, "twg", "bitbucket", "pull-requests", "get"),              // want forgecli
		// 6e853e0 put git fetch back on combined output: fetch reports its
		// progress and its errors on stderr, so a git read is another class.
		exec.Command("git", "--git-dir="+owner+".git", "fetch", "--prune", "origin"),
		// A program named in a parameter, as dc7377c open_review.go:27 did,
		// which no rule can see. Nothing flags this line, and nothing stops
		// it: a runner like that is kept on forgecli by convention, and the
		// fixture records the case as accepted.
		exec.CommandContext(ctx, program, args...),
	}
}
