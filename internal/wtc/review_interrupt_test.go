package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"taba.sh/taba/internal/retry"
)

// review487Launcher stands in for a reviewer client. In the phase named by
// REVIEW487_BLOCK it starts a descendant that would write a finding much later,
// records both pids, reports ready and waits; otherwise it writes its result.
// REVIEW487_IGNORE_TERM makes the descendant ignore SIGTERM and hold the
// output pipe, as a reviewer stuck in blocked I/O would.
const review487Launcher = `#!/bin/sh
ev="$REVIEW487_DIR"
id=$(basename "$3" .md)
phase=concern
[ "$id" = lead ] && phase=lead
echo "$phase" >> "$ev/started"
if [ "$REVIEW487_BLOCK" = "$phase" ]; then
  if [ -n "$REVIEW487_IGNORE_TERM" ]; then
    ( trap '' TERM; sleep 300; echo late > "$ev/late" ) &
  else
    ( sleep 300; printf '{"concern":"%s","status":"ok","findings":[]}\n' "$id" > "$4/findings/$id.json"; echo late > "$ev/late" ) &
  fi
  echo $! > "$ev/$phase.desc"
  echo $$ > "$ev/$phase.child"
  echo ready > "$ev/$phase.ready"
  wait
  exit 0
fi
if [ "$phase" = lead ]; then
  printf '**Local review: pass**\n' > "$4/summary.md"
  printf 'pass\n' > "$4/verdict"
  exit 0
fi
printf '{"concern":"%s","status":"ok","findings":[]}\n' "$id" > "$4/findings/$id.json"
`

type review487Fixture struct {
	c      *Context
	bundle string
	ev     string
	tmp    string
}

func newReview487Fixture(t *testing.T, public bool) review487Fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	collection := filepath.Join(root, "sample")
	app := filepath.Join(collection, "app")
	bundle := filepath.Join(collection, "bundle")
	bin := filepath.Join(root, "bin")
	ev := filepath.Join(root, "ev")
	tmp := filepath.Join(root, "tmp")
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{ev, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(collection, "harness", ".harness-repos.yml"), "repos:\n  - name: app\n    remote: https://github.com/example/app.git\n", 0644)
	write(filepath.Join(app, "code.go"), "package app\n", 0644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "fixture"}} {
		if out, err := exec.Command("git", append([]string{"-C", app}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	out, err := exec.Command("git", "-C", app, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(out))
	if err := os.MkdirAll(filepath.Join(bundle, "concerns"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeReviewManifest(bundle, ReviewManifest{Repo: "app", PR: "7", Forge: "github", Slug: "example/app", HeadSHA: head, Round: 1, RepoDir: app, Public: public}); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(bundle, "concerns", "code.md"), "---\nid: code\ntier: fast\n---\nReview code.\n", 0644)
	write(filepath.Join(bin, "gh"), "#!/bin/sh\necho '{\"headRefOid\":\""+head+"\",\"statusCheckRollup\":[]}'\n", 0755)
	write(filepath.Join(bin, "review-agent"), review487Launcher, 0755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", tmp)
	t.Setenv("WTC_FORGE_MIN_INTERVAL", "0")
	t.Setenv("WTC_REVIEW_AGENT_CMD", filepath.Join(bin, "review-agent"))
	// Public reviewers receive only allowlisted variables.
	t.Setenv("WTC_REVIEW_ENV", "REVIEW487_DIR REVIEW487_BLOCK REVIEW487_IGNORE_TERM")
	t.Setenv("REVIEW487_DIR", ev)
	t.Setenv("REVIEW487_BLOCK", "")
	t.Setenv("REVIEW487_IGNORE_TERM", "")
	c, err := OpenCollection(collection)
	if err != nil {
		t.Fatal(err)
	}
	return review487Fixture{c: c, bundle: bundle, ev: ev, tmp: tmp}
}

var review487Options = ReviewRunOptions{Strong: "test:", Standard: "test:", Fast: "test:", Lead: "test:", Parallel: 1}

// waitFor polls a read-only condition within a bounded deadline.
func review487WaitFor(t *testing.T, what string, within time.Duration, done func() bool) {
	t.Helper()
	err := retry.Poll(context.Background(), retry.Policy{Every: 10 * time.Millisecond, Within: within}, func(context.Context) (bool, error) {
		return done(), nil
	})
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func review487Pid(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		t.Fatalf("bad pid in %s: %q", path, data)
	}
	return pid
}

// review487Gone reports whether pid no longer exists. A killed process that
// was reparented is reaped by the system shortly after, so callers poll.
func review487Gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// review487Cleanup kills only the fixture's own recorded pids, in case an
// assertion failed while they were alive.
func review487Cleanup(t *testing.T, ev string) {
	t.Cleanup(func() {
		for _, name := range []string{"concern.child", "concern.desc", "lead.child", "lead.desc"} {
			if data, err := os.ReadFile(filepath.Join(ev, name)); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 1 {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
}

func review487Contexts(t *testing.T, tmp string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(tmp, reviewContextPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// Cancelling the caller's context stops the running reviewer and its
// descendant before the run returns, starts nothing more, removes an isolated
// context only after that, and leaves no summary or verdict to post.
func TestReviewRunCancelStopsOwnedReviewers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		public bool
		phase  string
	}{
		{"private-concern", false, "concern"},
		{"private-lead", false, "lead"},
		{"public-concern", true, "concern"},
		{"public-lead", true, "lead"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReview487Fixture(t, tc.public)
			review487Cleanup(t, f.ev)
			// A previous round's result must not survive as this run's.
			for name, body := range map[string]string{"summary.md": "**Local review: pass**\n", "verdict": "pass\n"} {
				if err := os.WriteFile(filepath.Join(f.bundle, name), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("REVIEW487_BLOCK", tc.phase)
			// An unrelated process the runner must leave alone.
			unrelated := exec.Command("sleep", "300")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() })

			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			type outcome struct {
				result ReviewRunResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := f.c.RunReviewBundleContext(ctx, f.bundle, review487Options)
				done <- outcome{result, err}
			}()
			review487WaitFor(t, "reviewer ready", 30*time.Second, func() bool {
				_, err := os.Stat(filepath.Join(f.ev, tc.phase+".ready"))
				return err == nil
			})
			child := review487Pid(t, filepath.Join(f.ev, tc.phase+".child"))
			desc := review487Pid(t, filepath.Join(f.ev, tc.phase+".desc"))
			if tc.public && len(review487Contexts(t, f.tmp)) != 1 {
				t.Fatal("expected one isolated context while the reviewer runs")
			}
			cancel(errors.New("synthetic interrupt"))
			var got outcome
			select {
			case got = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("run did not return after cancellation")
			}
			if !errors.Is(got.err, errReviewInterrupted) || !strings.Contains(got.err.Error(), "synthetic interrupt") || !strings.Contains(got.err.Error(), tc.phase) {
				t.Fatalf("run error = %v", got.err)
			}
			// Signalled and reaped before the run returned; reparenting can
			// delay only the system's reaping of the descendant.
			review487WaitFor(t, "reviewer process group stopped", 10*time.Second, func() bool {
				return review487Gone(child) && review487Gone(desc)
			})
			if err := unrelated.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("unrelated process was signalled: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.ev, "late")); !os.IsNotExist(err) {
				t.Fatalf("descendant wrote after cancellation: %v", err)
			}
			for _, name := range []string{"summary.md", "verdict"} {
				if _, err := os.Stat(filepath.Join(f.bundle, name)); !os.IsNotExist(err) {
					t.Fatalf("%s left for posting after cancellation: %v", name, err)
				}
			}
			var findings reviewFindingFile
			data, err := os.ReadFile(filepath.Join(f.bundle, "findings", "code.json"))
			if tc.phase == "concern" && (err != nil || json.Unmarshal(data, &findings) != nil || findings.Status != "error") {
				t.Fatalf("interrupted concern recorded as %s %v", data, err)
			}
			started, err := os.ReadFile(filepath.Join(f.ev, "started"))
			if err != nil || strings.Count(string(started), "\n") != map[string]int{"concern": 1, "lead": 2}[tc.phase] {
				t.Fatalf("reviewers started after cancellation: %q %v", started, err)
			}
			if left := review487Contexts(t, f.tmp); len(left) != 0 {
				t.Fatalf("isolated context left after cancellation: %v", left)
			}
			if tc.public {
				record, err := os.ReadFile(filepath.Join(f.bundle, reviewContextRecord))
				if err != nil || !strings.Contains(string(record), `"cleanup": "removed"`) {
					t.Fatalf("execution record: %s %v", record, err)
				}
			}

			// The next run owns the bundle: nothing from the stopped run
			// overwrites its result.
			t.Setenv("REVIEW487_BLOCK", "")
			next, err := f.c.RunReviewBundle(f.bundle, review487Options)
			if err != nil || next.Verdict != "pass" {
				t.Fatalf("next run: %+v %v", next, err)
			}
			data, err = os.ReadFile(filepath.Join(f.bundle, "findings", "code.json"))
			if err != nil || json.Unmarshal(data, &findings) != nil || findings.Status != "ok" {
				t.Fatalf("next run findings: %s %v", data, err)
			}
		})
	}
}

// A reviewer whose descendant ignores SIGTERM and keeps the output pipe open
// is killed after the bounded grace, with the rest of its group.
func TestReviewRunCancelEscalatesToKill(t *testing.T) {
	f := newReview487Fixture(t, false)
	review487Cleanup(t, f.ev)
	t.Setenv("REVIEW487_BLOCK", "concern")
	t.Setenv("REVIEW487_IGNORE_TERM", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.c.RunReviewBundleContext(ctx, f.bundle, review487Options)
		done <- err
	}()
	review487WaitFor(t, "reviewer ready", 30*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(f.ev, "concern.ready"))
		return err == nil
	})
	desc := review487Pid(t, filepath.Join(f.ev, "concern.desc"))
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(reviewStopGrace + 30*time.Second):
		t.Fatal("run did not return within the stop grace")
	}
	review487WaitFor(t, "TERM-ignoring descendant killed", 10*time.Second, func() bool { return review487Gone(desc) })
}

// A caller that is already cancelled starts no reviewer and no context.
func TestReviewRunCancelledBeforeStartLaunchesNothing(t *testing.T) {
	for _, public := range []bool{false, true} {
		f := newReview487Fixture(t, public)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.c.RunReviewBundleContext(ctx, f.bundle, review487Options); !errors.Is(err, context.Canceled) {
			t.Fatalf("public=%t: run error = %v", public, err)
		}
		if _, err := os.Stat(filepath.Join(f.ev, "started")); !os.IsNotExist(err) {
			t.Fatalf("public=%t: a reviewer started: %v", public, err)
		}
		if left := review487Contexts(t, f.tmp); len(left) != 0 {
			t.Fatalf("public=%t: context created: %v", public, left)
		}
	}
}

// A per-agent timeout keeps its own meaning: the reviewer's group is stopped,
// the concern is an error, and the run continues to the lead.
func TestReviewRunTimeoutStopsGroupAndContinues(t *testing.T) {
	f := newReview487Fixture(t, false)
	review487Cleanup(t, f.ev)
	t.Setenv("REVIEW487_BLOCK", "concern")
	opt := review487Options
	opt.Timeout = time.Second
	got, err := f.c.RunReviewBundle(f.bundle, opt)
	if err != nil || got.Verdict != "error" {
		t.Fatalf("timed-out concern: %+v %v", got, err)
	}
	data, err := os.ReadFile(filepath.Join(f.bundle, "findings", "code.json"))
	if err != nil || !strings.Contains(string(data), "timed out after 1s") {
		t.Fatalf("timeout not recorded: %s %v", data, err)
	}
	desc := review487Pid(t, filepath.Join(f.ev, "concern.desc"))
	review487WaitFor(t, "timed-out group stopped", 10*time.Second, func() bool { return review487Gone(desc) })
	if _, err := os.Stat(filepath.Join(f.bundle, "verdict")); err != nil {
		t.Fatalf("lead did not run after a timeout: %v", err)
	}
}
