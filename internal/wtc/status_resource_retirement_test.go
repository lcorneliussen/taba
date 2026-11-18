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
			snap.Schema = 1
			snap.Collection = filepath.Base(c.Collection)
			if err := c.WithStatusContext(ctx).WriteStatusSnapshot(snap); err == nil {
				t.Fatal("canceled retirement observation published")
			}
			for _, name := range []string{".wtc-status.json", ".wtc-status.md", ".last-wtc-status.yml"} {
				if _, err := os.Stat(filepath.Join(c.Collection, name)); !os.IsNotExist(err) {
					t.Fatal("canceled snapshot created", name, err)
				}
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

func TestStatusResourceRetirementFullReportEquivalence(t *testing.T) {
	for _, state := range []string{"clean", "untracked", "staged", "dirty", "rename", "detached", "named-detached", "unpushed", "conflict", "unborn"} {
		t.Run(state, func(t *testing.T) {
			c, target := retirementFixture(t)
			repo := filepath.Join(target, "widget")
			fixtureGit(t, "-C", repo, "switch", "-qc", "topic")
			commit := func() {
				fixtureGit(t, "-C", repo, "add", "-A")
				fixtureGit(t, "-C", repo, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "--allow-empty", "-qm", "Synthetic observation")
			}
			switch state {
			case "untracked", "staged":
				fixtureFile(t, filepath.Join(repo, "new"), "new\n", 0600)
				if state == "staged" {
					fixtureGit(t, "-C", repo, "add", "new")
				}
			case "dirty":
				fixtureFile(t, filepath.Join(repo, "README.md"), "dirty\n", 0600)
			case "rename":
				fixtureGit(t, "-C", repo, "mv", "README.md", "renamed")
			case "detached":
				fixtureGit(t, "-C", repo, "checkout", "-q", "--detach", "HEAD")
			case "named-detached":
				fixtureGit(t, "-C", repo, "switch", "-qc", "(detached)")
			case "unpushed":
				commit()
			case "unborn":
				fixtureGit(t, "-C", repo, "switch", "--orphan", "empty")
			case "conflict":
				fixtureGit(t, "-C", repo, "switch", "-qc", "other")
				fixtureFile(t, filepath.Join(repo, "README.md"), "other\n", 0600)
				commit()
				fixtureGit(t, "-C", repo, "switch", "topic")
				fixtureFile(t, filepath.Join(repo, "README.md"), "topic\n", 0600)
				commit()
				if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "merge", "other").CombinedOutput(); err == nil {
					t.Fatalf("conflict fixture did not conflict: %s", out)
				}
				if fixtureGit(t, "-C", repo, "ls-files", "-u") == "" {
					t.Fatal("missing real conflict")
				}
			}
			transcript := retirementTranscriptFixture(t, target, "Untrusted synthetic evidence; verification remains necessary.")
			compareRetirementReports(t, c, []string{transcript})
		})
	}
}

func compareRetirementReports(t *testing.T, c *Context, transcripts []string) {
	t.Helper()
	want, we := c.statusResourceLegacyInspect(context.Background(), "finished", transcripts)
	got, ge := c.InspectRetirementContext(context.Background(), "finished", transcripts)
	want.GeneratedAt = ""
	want.Assessment.ObservedAt = ""
	got.GeneratedAt = ""
	got.Assessment.ObservedAt = ""
	if we != nil || ge != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("complete report drift: got%+v err%v want%+v err%v", got, ge, want, we)
	}
}

func TestStatusResourceRetirementTransitionsReadAgain(t *testing.T) {
	c, target := retirementFixture(t)
	repo := filepath.Join(target, "widget")
	fixtureGit(t, "-C", repo, "switch", "-qc", "topic")
	compareRetirementReports(t, c, nil)
	fixtureFile(t, filepath.Join(repo, "README.md"), "dirty\n", 0600)
	compareRetirementReports(t, c, nil)
	fixtureGit(t, "-C", repo, "add", "-A")
	fixtureGit(t, "-C", repo, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "Synthetic follow-up")
	compareRetirementReports(t, c, nil)
	fixtureGit(t, "-C", repo, "switch", "-qc", "next")
	compareRetirementReports(t, c, nil)
	fixtureGit(t, "-C", repo, "checkout", "-q", "--detach", "HEAD")
	compareRetirementReports(t, c, nil)
}

func TestStatusResourceRetirementMalformedAndUnsupportedFallback(t *testing.T) {
	for _, mode := range []string{"unsupported", "malformed", "unborn-header", "duplicate-header", "unknown-record", "bad-tracked-record", "empty-untracked-record"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := retirementFixture(t)
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			t.Setenv("RETIRE_TEST_GIT", real)
			t.Setenv("RETIRE_TEST_MODE", mode)
			fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
case " $* " in
 *" --no-optional-locks "*" status --porcelain=v2 --branch "*)
  case "$RETIRE_TEST_MODE" in
   unsupported) exit 129 ;;
   malformed) printf 'malformed\n' ;;
   unborn-header) printf '# branch.oid (initial)\n# branch.head main\n' ;;
   duplicate-header) printf '# branch.oid aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n# branch.oid bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n# branch.head main\n' ;;
   unknown-record) printf '# branch.oid aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n# branch.head main\nunknown record\n' ;;
   bad-tracked-record) printf '# branch.oid aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n# branch.head main\n1 broken\n' ;;
   empty-untracked-record) printf '# branch.oid aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n# branch.head main\n? \n' ;;
  esac
  exit 0 ;;
esac
exec "$RETIRE_TEST_GIT" "$@"
`, 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			compareRetirementReports(t, c, nil)
		})
	}
}

func TestStatusResourceRetirementBatchAndFallbackShareCallerBudget(t *testing.T) {
	for _, phase := range []string{"batch", "fallback"} {
		t.Run(phase, func(t *testing.T) {
			c := newWorkspaceFixture(t)
			bin := t.TempDir()
			calls := filepath.Join(bin, "calls")
			t.Setenv("RETIRE_TEST_CALLS", calls)
			t.Setenv("RETIRE_TEST_PHASE", phase)
			fixtureFile(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "$1" = --fixture-probe ]; then printf '%s' "$RETIRE_TEST_CALLS"; exit 0; fi
printf '%s\n' "$*" >> "$RETIRE_TEST_CALLS"
case " $* " in
 *" status --porcelain=v2 --branch "*)
  if [ "$RETIRE_TEST_PHASE" = fallback ]; then printf 'malformed\n'; exit 0; fi ;;
esac
exec /bin/sleep 10
`, 0700)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			probeStart := time.Now()
			probe, probeErr := exec.Command("git", "--fixture-probe").CombinedOutput()
			looked, _ := exec.LookPath("git")
			if probeErr != nil || string(probe) != calls || looked != filepath.Join(bin, "git") {
				t.Fatalf("wrong fixture: %q %v %s", probe, probeErr, looked)
			}
			t.Logf("fixture admission %s", time.Since(probeStart))
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			row := statusResourceRetirementRead(ctx, c.Harness)
			if row.Error == "" || !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Now().After(deadline.Add(time.Second)) {
				t.Fatalf("unbounded or authoritative canceled row: %+v", row)
			}
			b, _ := os.ReadFile(calls)
			want := 1
			if phase == "fallback" {
				want = 2
			}
			if strings.Count(string(b), "\n") != want {
				t.Fatal("fallback/count dispatched after deadline", string(b))
			}
			time.Sleep(40 * time.Millisecond)
			after, _ := os.ReadFile(calls)
			if string(b) != string(after) {
				t.Fatal("late dispatch")
			}
		})
	}
}
