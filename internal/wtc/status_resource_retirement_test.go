package wtc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
