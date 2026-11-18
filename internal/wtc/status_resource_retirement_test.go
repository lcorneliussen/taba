package wtc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusResourceRetirementCancellation(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "before-dispatch"
		if held {
			name = "held-child"
		}
		t.Run(name, func(t *testing.T) {
			c, _ := retirementFixture(t)
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			release := filepath.Join(bin, "release")
			t.Setenv("RETIRE_TEST_GIT", real)
			t.Setenv("RETIRE_TEST_CALLS", calls)
			t.Setenv("RETIRE_TEST_RELEASE", release)
			fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$1" = --no-optional-locks ]; then
 printf '%s\n' "$*" >> "$RETIRE_TEST_CALLS"
 if [ "$RETIRE_TEST_HOLD" = 1 ]; then
  while [ ! -f "$RETIRE_TEST_RELEASE" ]; do sleep .01; done
 fi
fi
exec "$RETIRE_TEST_GIT" "$@"
`, 0755)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if held {
				t.Setenv("RETIRE_TEST_HOLD", "1")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !held {
				cancel()
			}
			done := make(chan StatusSnapshot, 1)
			go func() {
				var snap StatusSnapshot
				c.WithStatusContext(ctx).appendRetirement(&snap, "finished")
				done <- snap
			}()
			if held {
				deadline := time.Now().Add(3 * time.Second)
				for {
					if b, _ := os.ReadFile(calls); len(b) > 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("guarded child not entered")
					}
					time.Sleep(5 * time.Millisecond)
				}
				cancel()
			}
			var snap StatusSnapshot
			select {
			case snap = <-done:
			case <-time.After(time.Second):
				fixtureFile(t, release, "release", 0600)
				<-done
				t.Fatal("retirement read ignored caller cancellation")
			}
			before, _ := os.ReadFile(calls)
			time.Sleep(40 * time.Millisecond)
			after, _ := os.ReadFile(calls)
			if string(before) != string(after) {
				t.Fatal("postcancel Git dispatch")
			}
			if !held && len(before) > 0 {
				t.Fatal("canceled-before-dispatch launched guarded Git")
			}
			if held && strings.Count(string(before), "--no-optional-locks") != 1 {
				t.Fatalf("continued guarded reads: %s", before)
			}
			if len(snap.Retirement) != 1 || snap.Retirement[0].State != "unknown" || len(snap.Retirement[0].Unknowns) == 0 {
				t.Fatalf("canceled evidence gained authority: %+v", snap.Retirement)
			}
		})
	}
}

func TestStatusResourceRetirementBackgroundCompatibility(t *testing.T) {
	c, _ := retirementFixture(t)
	a, ae := c.InspectRetirement("finished", nil)
	b, be := c.InspectRetirementContext(context.Background(), "finished", nil)
	a.GeneratedAt = ""
	a.Assessment.ObservedAt = ""
	b.GeneratedAt = ""
	b.Assessment.ObservedAt = ""
	if ae != nil || be != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("compatibility changed: %+v %v vs %+v %v", a, ae, b, be)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.InspectRetirementContext(ctx, "finished", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := retirementGitContext(ctx, c.Harness, "rev-parse", "HEAD"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestStatusResourceRetirementBatchesGuardedLocalReads(t *testing.T) {
	c, target := retirementFixture(t)
	for _, dir := range []string{"harness", "widget"} {
		fixtureGit(t, "-C", filepath.Join(target, dir), "switch", "-qc", "topic")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("RETIRE_TEST_GIT", real)
	t.Setenv("RETIRE_TEST_CALLS", calls)
	fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$1" = --no-optional-locks ]; then printf '%s\n' "$*" >> "$RETIRE_TEST_CALLS"; fi
exec "$RETIRE_TEST_GIT" "$@"
`, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	report, err := c.InspectRetirementContext(context.Background(), "finished", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 2*len(report.Repos) {
		t.Fatalf("guarded local reads=%d for%d named-branch repos; want2 each", n, len(report.Repos))
	}
	for _, r := range report.Repos {
		if r.Error != "" || r.Dirty || r.Unpushed != 0 || r.Branch != "topic" {
			t.Fatalf("lost current facts: %+v", r)
		}
	}
}
