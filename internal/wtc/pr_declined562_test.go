package wtc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordedDeclinesInThePRRegistry(t *testing.T) {
	c := newWorkspaceFixture(t)
	registry := filepath.Join(c.Collection, ".wtc-prs")
	fixtureFile(t, registry, prHeader+
		"r1 12 topic-12 https://bitbucket.org/example/r1/pull-requests/12 Synthetic 12\n"+
		"r1 14 topic-14 https://github.com/example/r1/pull/14 Synthetic 14\n"+
		"r1 15 topic-15 https://bitbucket.org/example/r1/pull-requests/15 Synthetic 15\n"+
		"# declined-pr r1 15 not-a-time\n", 0600)
	records, err := c.ListPRs()
	if err != nil || len(records) != 3 || records[2].DeclinedOn != "" {
		t.Fatalf("malformed decline row accepted: %+v %v", records, err)
	}
	declined := statusPRDetail{State: "DECLINED", ClosedOn: "2026-11-20T10:00:00.123456+00:00", ClosedAt: "2026-11-20T09:59:00Z", Freshness: "live"}
	details := []statusPRDetail{declined, declined, {State: "DECLINED", Freshness: "unavailable", ClosedOn: "2026-11-20T10:00:00Z"}}
	// Only Bitbucket declines are recorded; a GitHub close can be reopened
	// without this list check. An unavailable observation records nothing.
	recorded, cleared, err := c.recordDeclinedPRs(records, details, []bool{true, false, true})
	if err != nil || recorded != 1 || cleared != 0 {
		t.Fatalf("recorded=%d cleared=%d err=%v", recorded, cleared, err)
	}
	if records, err = c.ListPRs(); err != nil || records[0].DeclinedOn != declined.ClosedOn || records[0].DeclinedAt != declined.ClosedAt || records[1].DeclinedOn != "" || records[2].DeclinedOn != "" {
		t.Fatalf("records after recording: %+v %v", records, err)
	}
	// The same decline again changes nothing.
	if recorded, cleared, err := c.recordDeclinedPRs(records, details, []bool{true, false, true}); err != nil || recorded != 0 || cleared != 0 {
		t.Fatalf("repeat: recorded=%d cleared=%d err=%v", recorded, cleared, err)
	}
	// A registry-sourced decline is never re-recorded from itself.
	if recorded, _, err := c.recordDeclinedPRs(records[:1], []statusPRDetail{statusRecordedDeclineDetail(records[0])}, []bool{true}); err != nil || recorded != 0 {
		t.Fatalf("registry detail re-recorded: %d %v", recorded, err)
	}
	// Unlisting drops the decline with the PR.
	if err := c.UnlistPR("r1", "12"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(registry)
	if err != nil || strings.Contains(string(raw), "declined-pr r1 12") || !strings.Contains(string(raw), "# declined-pr r1 15 not-a-time") {
		t.Fatalf("unlist left the decline or dropped unrelated rows:\n%s %v", raw, err)
	}
}
