package next

import (
	"strings"
	"testing"
)

func TestDirtyGatewayOffersCatchUpAndCommit(t *testing.T) {
	f := Facts{Repo: "example", Host: HostOpenClaw, Branch: "openclaw/example", Dirty: 1, Behind: 5}
	c := ChoicesFor(f)
	if c.Primary() == nil || c.Primary().Step != "catch-up" {
		t.Fatalf("dirty gateway catch-up not offered: %+v", c)
	}
	seen := map[string]bool{}
	for _, a := range c.Actions {
		seen[a.Step.Step] = a.Available
	}
	if !seen["commit"] || !seen["catch-up"] {
		t.Fatalf("missing alternative: %+v", c)
	}
	f.Host = HostLocal
	if c := ChoicesFor(f); c.Primary() == nil || c.Primary().Step != "commit" {
		t.Fatalf("local policy changed: %+v", c)
	}
	f.Host = HostOpenClaw
	f.InProgress = "local changes recovery"
	if c := ChoicesFor(f); c.Primary() == nil || c.Primary().Step != "resolve" || len(c.Actions) != 1 {
		t.Fatalf("recovery can be retried: %+v", c)
	}
}

func TestDirtyConflictingGatewayHandoffPreservesBeforeMerge(t *testing.T) {
	c := ChoicesFor(Facts{Repo: "example", Host: HostOpenClaw, Branch: "openclaw/example", Dirty: 1, Behind: 2, MergeConflicts: []string{"notes.md"}})
	p := c.Primary()
	if p == nil || p.Step != "merge" || !strings.Contains(p.Reason, "Before git merge") || !strings.Contains(p.Reason, "verify the checkout is clean") || !strings.Contains(p.Reason, "unstaged and untracked") {
		t.Fatalf("unsafe dirty merge handoff: %+v", p)
	}
}
