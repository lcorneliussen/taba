package wtc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTunnelRetirementAdmissionOwnershipInterruptedRetryAndUnknownRefusal(t *testing.T) {
	c := runtimeFixture(t)
	ctx := context.Background()
	first, err := c.beginTunnelRetirement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.beginTunnelRetirement(ctx); err == nil {
		t.Fatal("concurrent attempt stole live retirement admission")
	}
	if err = c.endTunnelRetirement(ctx, "foreign-attempt", true); err == nil {
		t.Fatal("foreign attempt released admission")
	}
	if err = c.endTunnelRetirement(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	second, err := c.beginTunnelRetirement(ctx)
	if err != nil || second == first {
		t.Fatal("interrupted attempt did not admit bounded retry", err)
	}
	if err = c.endTunnelRetirement(ctx, first, true); err == nil {
		t.Fatal("old owner released retry admission")
	}
	if err = c.endTunnelRetirement(ctx, second, true); err != nil {
		t.Fatal(err)
	}
	if err = c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error { s.Retiring = true; s.Retirement = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = c.beginTunnelRetirement(ctx); err == nil {
		t.Fatal("unknown retirement owner silently reset")
	}
}
func TestTunnelRetirementAdmissionPositiveEndedInstanceRetry(t *testing.T) {
	c := runtimeFixture(t)
	ctx := context.Background()
	id, err := c.beginTunnelRetirement(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic recorded birth differs from this live PID. The prior instance is
	// positively gone; the current process is never signalled or adopted.
	if err = c.tunnelRuntimeStore().Update(ctx, func(s *tunnelState) error { s.Retirement.Process.Start = "synthetic-ended-instance"; return nil }); err != nil {
		t.Fatal(err)
	}
	retry, err := c.beginTunnelRetirement(ctx)
	if err != nil || retry == id {
		t.Fatal("ended owner instance prevented retry", err)
	}
	if err = c.endTunnelRetirement(ctx, retry, true); err != nil {
		t.Fatal(err)
	}
}
func TestTunnelRetirementAbortedLegacyHookReleasesAdmissionForIntactReceiver(t *testing.T) {
	c := newWorkspaceFixture(t)
	made, err := c.NewCollection(NewOptions{Slug: "retry-retirement", Repos: []string{"widget"}})
	if err != nil {
		t.Fatal(err)
	}
	target, err := OpenCollection(made.Collection)
	if err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, filepath.Join(c.Harness, "hooks/wtc/retire.pre.sh"), "#!/bin/sh\nexit 73\n", 0755)
	if _, err = c.RetireCollection(RetireOptions{Name: "retry-retirement", Force: true}); err == nil {
		t.Fatal("legacy hook failure ignored")
	}
	state, err := target.readTunnelJournal()
	if err != nil || state.Retiring || state.Retirement != nil {
		t.Fatal("aborted intact retirement left permanent admission fence", err)
	}
	if _, err = os.Stat(filepath.Join(target.Collection, "widget")); err != nil {
		t.Fatal("failed pre-hook deleted member", err)
	}
	fixtureFile(t, filepath.Join(c.Harness, "hooks/wtc/retire.pre.sh"), "#!/bin/sh\nexit 0\n", 0755)
	result, err := c.RetireCollection(RetireOptions{Name: "retry-retirement", Force: true})
	if err != nil || !result.FolderRemoved {
		t.Fatal("safe retry failed", result, err)
	}
}
